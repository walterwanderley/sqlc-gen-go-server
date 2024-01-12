package golang

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"github.com/sqlc-dev/sqlc-gen-go/internal/opts"
	"github.com/walterwanderley/sqlc-grpc/converter"
	grpctemplates "github.com/walterwanderley/sqlc-grpc/templates"
)

func grpcFiles(req *plugin.GenerateRequest, options *opts.Options, enums []Enum, structs []Struct, queries []Query) ([]*plugin.File, error) {
	def := toServerDefinition(req, options, enums, structs, queries)
	if err := def.Validate(); err != nil {
		return nil, err
	}
	pkg := def.Packages[0]
	depth := make([]string, 0)
	for i := 0; i < len(strings.Split(options.Out, string(filepath.Separator)))+1; i++ {
		depth = append(depth, "..")
	}
	toRootPath := filepath.Join(depth...)
	files := make([]*plugin.File, 0)
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
			protoContent, err := execServerTemplate(grpctemplates.Files, grpctemplates.Funcs, path, pkg, false)
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
			content, err := execServerTemplate(grpctemplates.Files, grpctemplates.Funcs, path, pkg, true)
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

		if strings.HasSuffix(newPath, "litestream.go") && !(def.Database() == "sqlite" && def.Litestream) {
			return nil
		}

		if (strings.HasSuffix(newPath, "litefs.go") || strings.HasSuffix(newPath, "forward.go")) && !(def.Database() == "sqlite" && def.LiteFS) {
			return nil
		}

		content, err := execServerTemplate(grpctemplates.Files, grpctemplates.Funcs, path, def, strings.HasSuffix(newPath, ".go"))
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
