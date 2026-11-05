package handoff

import (
	"embed"
	"io/fs"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

//go:embed handoff.go json.go identity.go executor.go cmd/cel-handoff/*.go go.mod go.sum
var implementation embed.FS
var buildOnce sync.Once
var buildRef string

func BuildID() string {
	buildOnce.Do(func() {
		var body strings.Builder
		fs.WalkDir(implementation, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				raw, _ := implementation.ReadFile(path)
				body.WriteString(path + "\x00" + string(raw) + "\x00")
			}
			return nil
		})
		body.WriteString(runtime.Version() + "\x00" + runtime.GOOS + "/" + runtime.GOARCH)
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, dep := range info.Deps {
				body.WriteString("\x00" + dep.Path + "@" + dep.Version + ":" + dep.Sum)
				if dep.Replace != nil {
					body.WriteString("=>" + dep.Replace.Path + "@" + dep.Replace.Version + ":" + dep.Replace.Sum)
				}
			}
			for _, s := range info.Settings {
				if !strings.HasPrefix(s.Key, "vcs") {
					body.WriteString("\x00" + s.Key + "=" + s.Value)
				}
			}
		}
		buildRef = Digest("application/vnd.cel-handoff.implementation", []byte(body.String()))
	})
	return buildRef
}
