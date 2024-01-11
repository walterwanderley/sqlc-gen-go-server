package golang

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"github.com/sqlc-dev/sqlc-gen-go/internal/opts"
	"github.com/walterwanderley/sqlc-grpc/converter"
	"github.com/walterwanderley/sqlc-grpc/metadata"
	grpctemplates "github.com/walterwanderley/sqlc-grpc/templates"
	"golang.org/x/tools/imports"
)

func grpcFiles(req *plugin.GenerateRequest, options *opts.Options, enums []Enum, structs []Struct, queries []Query) ([]*plugin.File, error) {
	module := options.Module
	if module == "" {
		module = "my-project"
	}
	sqlPackage := options.SqlPackage
	if sqlPackage == "" {
		sqlPackage = "database/sql"
	}
	files := make([]*plugin.File, 0)
	messages := make(map[string]*metadata.Message)
	for _, st := range structs {
		msg := metadata.Message{
			Name: st.Name,
		}
		for _, field := range st.Fields {
			msg.Fields = append(msg.Fields, &metadata.Field{
				Name: field.Name,
				Type: field.Type,
			})
		}
		messages[st.Name] = &msg
	}
	services := make([]*metadata.Service, 0)
	var hasExecResult bool
	for _, query := range queries {
		inputNames := make([]string, 0)
		inputTypes := make([]string, 0)
		if query.Arg.Struct != nil {
			fields := make([]*metadata.Field, 0)
			for _, f := range query.Arg.Struct.Fields {
				fields = append(fields, &metadata.Field{
					Name: f.Name,
					Type: f.Type,
				})
			}
			msg := metadata.Message{
				Name:   query.Arg.Struct.Name,
				Fields: fields,
			}
			messages[query.Arg.Struct.Name] = &msg
		} else {
			typeName := query.MethodName + "Params"
			var fields []*metadata.Field
			if !query.Arg.isEmpty() {
				fields = append(fields, &metadata.Field{
					Name: query.Arg.Name,
					Type: query.Arg.Typ,
				})
			}
			messages[typeName] = &metadata.Message{
				Name:   typeName,
				Fields: fields,
			}
		}
		if !query.Arg.isEmpty() {
			inputNames = append(inputNames, query.Arg.Name)
			var typ strings.Builder
			if query.Arg.EmitPointer {
				typ.WriteString("*")
			}
			typ.WriteString(query.Arg.Type())
			inputTypes = append(inputTypes, typ.String())
		}

		var retFields []*metadata.Field
		if !query.Ret.isEmpty() {
			retField := metadata.Field{
				Name: "value",
			}
			isArray := query.Cmd == ":many"
			if isArray {
				retField.Name = "list"
			} else {
				if query.Ret.Struct != nil {
					retField.Name = query.Ret.Struct.Name
				}
			}
			var typ strings.Builder
			if isArray {
				typ.WriteString("[]")
			}
			if query.Ret.EmitPointer {
				typ.WriteString("*")
			}
			typ.WriteString(query.Ret.Type())
			retField.Type = converter.ToProtoType(typ.String())

			retFields = append(retFields, &retField)
		} else if query.Cmd == ":execresult" {
			hasExecResult = true
			retFields = append(retFields, &metadata.Field{
				Name: "value",
				Type: converter.ToProtoType("sql.Result"),
			})
		}
		retMessage := metadata.Message{
			Name:   query.MethodName + "Response",
			Fields: retFields,
		}
		messages[retMessage.Name] = &retMessage
		var out strings.Builder
		if !query.Ret.isEmpty() {
			if query.Cmd == ":many" {
				out.WriteString("[]")
			}
			out.WriteString(query.Ret.Type())
		} else if query.Cmd == ":execresult" {
			out.WriteString("sql.Result")
		}
		services = append(services, &metadata.Service{
			Name:       query.MethodName,
			Sql:        query.SQL,
			Messages:   messages,
			Output:     out.String(),
			InputNames: inputNames,
			InputTypes: inputTypes,
		})
	}
	sort.SliceStable(services, func(i, j int) bool {
		return strings.Compare(services[i].Name, services[j].Name) < 0
	})
	pkg := metadata.Package{
		Messages:           messages,
		Services:           services,
		Engine:             req.Settings.Engine,
		SqlPackage:         sqlPackage,
		EmitInterface:      options.EmitInterface,
		EmitParamsPointers: options.EmitParamsStructPointers,
		EmitResultPointers: options.EmitResultStructPointers,
		EmitDbArgument:     options.EmitMethodsWithDbArgument,
		Package:            options.Package,
		SrcPath:            req.Settings.Codegen.Out,
		HasExecResult:      hasExecResult,
		GoModule:           module,
	}

	def := metadata.Definition{
		GoModule: module,
		Packages: []*metadata.Package{
			&pkg,
		},
		LiteFS:             options.LiteFS,
		DistributedTracing: options.Tracing,
		MigrationPath:      options.MigrationPath,
	}

	outAdapters := make(map[string]struct{})

	for _, s := range pkg.Services {
		if s.HasCustomOutput() || s.HasArrayOutput() {
			outAdapters[canonicalName(s.Output)] = struct{}{}
		}
	}

	pkg.OutputAdapters = make([]*metadata.Message, len(outAdapters))
	i := 0
	for k := range outAdapters {
		pkg.OutputAdapters[i] = pkg.Messages[k]
		i++
	}

	sort.SliceStable(pkg.OutputAdapters, func(i, j int) bool {
		return strings.Compare(pkg.OutputAdapters[i].Name, pkg.OutputAdapters[j].Name) < 0
	})

	depth := make([]string, 0)
	for i := 0; i < len(strings.Split(options.Out, string(filepath.Separator)))+1; i++ {
		depth = append(depth, "..")
	}
	toRootPath := filepath.Join(depth...)
	err := fs.WalkDir(grpctemplates.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Println("ERROR ", err.Error())
			return err
		}

		if d.IsDir() {
			return nil
		}

		newPath := strings.TrimSuffix(path, ".tmpl")

		if strings.HasSuffix(newPath, "templates.go") {
			return nil
		}
		log.Println(path, "...")
		if strings.HasSuffix(newPath, "service.proto") {
			protoContent, err := execServerTemplate(grpctemplates.Files, path, &pkg, false)
			if err != nil {
				return err
			}
			protoFile := filepath.Join(toRootPath, "proto", converter.ToSnakeCase(pkg.Package), "v1", (converter.ToSnakeCase(pkg.Package) + ".proto"))
			files = append(files, &plugin.File{
				Name:     protoFile,
				Contents: protoContent,
			})
			return nil
		}

		if strings.HasSuffix(newPath, "adapters.go") || strings.HasSuffix(newPath, "service.go") || strings.HasSuffix(newPath, "service.factory.go") {
			content, err := execServerTemplate(grpctemplates.Files, path, &pkg, true)
			if err != nil {
				return err
			}
			files = append(files, &plugin.File{
				Name:     newPath,
				Contents: content,
			})
			return nil
		}

		if strings.HasSuffix(newPath, "tracing.go") && !def.DistributedTracing {
			return nil
		}

		if strings.HasSuffix(newPath, "migration.go") && def.MigrationPath == "" {
			return nil
		}

		if strings.HasSuffix(newPath, "litestream.go") && def.Database() != "sqlite" {
			return nil
		}

		if (strings.HasSuffix(newPath, "litefs.go") || strings.HasSuffix(newPath, "forward.go")) && !(def.Database() == "sqlite" && def.LiteFS) {
			return nil
		}

		content, err := execServerTemplate(grpctemplates.Files, path, &def, strings.HasSuffix(newPath, ".go"))
		if err != nil {
			return err
		}
		files = append(files, &plugin.File{
			Name:     filepath.Join(toRootPath, newPath),
			Contents: content,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}
	if !options.SkipGoMod {
		files = append(files, &plugin.File{
			Name:     filepath.Join(toRootPath, "go.mod"),
			Contents: []byte(fmt.Sprintf("module %s\n", module)),
		})
	}

	return files, nil
}

func execServerTemplate(fs fs.FS, name string, data any, goSource bool) ([]byte, error) {
	var b bytes.Buffer

	funcMap := template.FuncMap{
		"PascalCase": converter.ToPascalCase,
		"SnakeCase":  converter.ToSnakeCase,
	}

	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tmpl, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	t, err := template.New(name).Funcs(funcMap).Parse(string(tmpl))
	if err != nil {
		return nil, err
	}
	err = t.Execute(&b, data)
	if err != nil {
		return nil, fmt.Errorf("execute template error: %w", err)
	}

	var src []byte
	if goSource {
		src, err = format.Source(b.Bytes())
		if err != nil {
			log.Println(b.String())
			return nil, fmt.Errorf("format source error: %w", err)
		}
		src, err = imports.Process("", src, nil)
		if err != nil {
			return nil, fmt.Errorf("organize imports error: %w", err)
		}
	} else {
		src = b.Bytes()
	}
	return src, nil
}

func canonicalName(typ string) string {
	name := strings.TrimPrefix(typ, "[]")
	name = strings.TrimPrefix(name, "*")
	return name
}
