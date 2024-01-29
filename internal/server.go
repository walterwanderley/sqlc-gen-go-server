package golang

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"github.com/sqlc-dev/sqlc-gen-go/internal/opts"
	connecttemplates "github.com/walterwanderley/sqlc-connect/templates"
	"github.com/walterwanderley/sqlc-grpc/converter"
	"github.com/walterwanderley/sqlc-grpc/metadata"
	grpctemplates "github.com/walterwanderley/sqlc-grpc/templates"
	httptemplates "github.com/walterwanderley/sqlc-http/templates"
)

func serverFiles(req *plugin.GenerateRequest, options *opts.Options, enums []Enum, structs []Struct, queries []Query) ([]*plugin.File, error) {
	var (
		tmplFS    fs.FS
		tmplFuncs template.FuncMap
	)

	switch options.ServerType {
	case "grpc":
		tmplFS = grpctemplates.Files
		tmplFuncs = grpctemplates.Funcs
	case "connect":
		tmplFS = connecttemplates.Files
		tmplFuncs = connecttemplates.Funcs
	case "", "http": // the default server type
		tmplFS = httptemplates.Files
		tmplFuncs = httptemplates.Funcs
	default:
		return nil, fmt.Errorf("invalid server_type %q. Choose 'connect', 'grpc' or 'http'", options.ServerType)
	}
	def := toServerDefinition(req, options, enums, structs, queries)
	if err := def.Validate(); err != nil {
		return nil, err
	}
	pkg := def.Packages[0]
	depth := make([]string, 0)
	for i := 0; i < len(strings.Split(req.GetSettings().GetCodegen().GetOut(), string(filepath.Separator))); i++ {
		depth = append(depth, "..")
	}
	toRootPath := filepath.Join(depth...)
	files := make([]*plugin.File, 0)
	err := fs.WalkDir(tmplFS, ".", func(path string, d fs.DirEntry, err error) error {
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
			protoContent, err := execServerTemplate(tmplFS, tmplFuncs, path, pkg, false)
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

		if strings.HasSuffix(newPath, "adapters.go") || strings.HasSuffix(newPath, "service.go") ||
			strings.HasSuffix(newPath, "service.factory.go") || strings.HasSuffix(newPath, "routes.go") {
			if options.Append && strings.HasSuffix(newPath, "service.factory.go") {
				return nil
			}
			content, err := execServerTemplate(tmplFS, tmplFuncs, path, pkg, true)
			if err != nil {
				return err
			}
			files = append(files, &plugin.File{
				Name:     newPath,
				Contents: content,
			})
			return nil
		}

		if options.Append {
			return nil
		}

		if strings.HasSuffix(newPath, "metric.go") && !def.Metric {
			return nil
		}

		if strings.HasSuffix(newPath, "tracing.go") && !def.DistributedTracing {
			return nil
		}

		if strings.HasSuffix(newPath, "migration.go") && def.MigrationPath == "" {
			return nil
		}

		if strings.HasSuffix(newPath, "litestream.go") && !def.Litestream {
			return nil
		}

		if (strings.HasSuffix(newPath, "litefs.go") || strings.HasSuffix(newPath, "forward.go")) && !def.LiteFS {
			return nil
		}

		content, err := execServerTemplate(tmplFS, tmplFuncs, path, def, strings.HasSuffix(newPath, ".go"))
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
			Contents: []byte(fmt.Sprintf("module %s\n", def.GoModule)),
		})
	}

	return files, nil
}

func toServerDefinition(req *plugin.GenerateRequest, options *opts.Options, enums []Enum, structs []Struct, queries []Query) *metadata.Definition {
	module := options.Module
	if module == "" {
		module = "my-project"
	}
	sqlPackage := options.SqlPackage
	if sqlPackage == "" {
		sqlPackage = "database/sql"
	}
	migrationLib := options.MigrationLib
	if migrationLib == "" {
		migrationLib = "goose"
	}
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
	queriesToSkip := make([]*regexp.Regexp, 0)
	for _, queryName := range strings.Split(options.SkipQueries, ",") {
		s := strings.TrimSpace(queryName)
		if s == "" {
			continue
		}
		queriesToSkip = append(queriesToSkip, regexp.MustCompile(s))
	}
	services := make([]*metadata.Service, 0)
	var hasExecResult bool
	for _, query := range queries {
		var skip bool
		for _, re := range queriesToSkip {
			if re.MatchString(query.MethodName) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
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
			retField.Type = typ.String()

			retFields = append(retFields, &retField)
		} else if query.Cmd == ":execresult" {
			hasExecResult = true
			retFields = append(retFields, &metadata.Field{
				Name: "value",
				Type: "sql.Result",
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
		httpSpecs := make([]metadata.HttpSpec, 0)
		for _, doc := range query.Comments {
			doc = strings.TrimSpace(doc)
			if strings.HasPrefix(doc, "http: ") {
				opts := strings.Split(strings.TrimPrefix(doc, "http: "), " ")
				if len(opts) != 2 {
					continue
				}
				httpMethod, httpPath := strings.ToUpper(opts[0]), opts[1]
				switch httpMethod {
				case "POST", "GET", "PUT", "DELETE", "PATCH":
				default:
					continue
				}
				httpSpecs = append(httpSpecs, metadata.HttpSpec{
					Method: httpMethod,
					Path:   httpPath,
				})
			}
		}
		services = append(services, &metadata.Service{
			Name:       query.MethodName,
			Sql:        query.SQL,
			Messages:   messages,
			Output:     out.String(),
			InputNames: inputNames,
			InputTypes: inputTypes,
			HttpSpecs:  httpSpecs,
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
		Litestream:         options.Litestream,
		DistributedTracing: options.Tracing,
		Metric:             options.Metric,
		MigrationPath:      options.MigrationPath,
		MigrationLib:       migrationLib,
	}

	outAdapters := make(map[string]struct{})

	for _, s := range pkg.Services {
		if s.HasCustomOutput() || s.HasArrayOutput() {
			outAdapters[converter.CanonicalName(s.Output)] = struct{}{}
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

	return &def
}

func execServerTemplate(fs fs.FS, funcs template.FuncMap, name string, data any, goSource bool) ([]byte, error) {
	var b bytes.Buffer

	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tmpl, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	t, err := template.New(name).Funcs(funcs).Parse(string(tmpl))
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
	} else {
		src = b.Bytes()
	}
	return src, nil
}
