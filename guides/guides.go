// Package guides holds the app guides of Pinky's own agent: short notes on
// how a common app is laid out, one Markdown file each, which a task reads
// when it opens that app (CONTRIBUTING.md has the format).
package guides

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

//go:embed *.md
var files embed.FS

// MaxBody is the most characters a guide's body may have: every line
// goes to the model on each task in the app.
const MaxBody = 1000

// Guide is one app's guide.
type Guide struct {
	ID      string   // the file's name without .md
	App     string   // the name it goes by
	Names   []string // its names on phones, for phones that do not say its id
	Android []string // package names
	IOS     []string // bundle ids
	Body    string   // the list of lines
}

type front struct {
	App     string   `yaml:"app"`
	Names   []string `yaml:"names"`
	Android []string `yaml:"android"`
	IOS     []string `yaml:"ios"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse reads a guide from its file.
func Parse(id string, data []byte) (Guide, error) {
	if !idPattern.MatchString(id) {
		return Guide{}, fmt.Errorf("guide %q: the file name must be lowercase letters, digits and hyphens", id)
	}
	rest, ok := bytes.CutPrefix(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("---\n"))
	if !ok {
		return Guide{}, fmt.Errorf("guide %q: no front matter", id)
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Guide{}, fmt.Errorf("guide %q: the front matter does not end", id)
	}
	var f front
	dec := yaml.NewDecoder(bytes.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Guide{}, fmt.Errorf("guide %q: %w", id, err)
	}
	g := Guide{ID: id, App: strings.TrimSpace(f.App), Names: f.Names, Android: f.Android, IOS: f.IOS,
		Body: strings.TrimSpace(string(body))}
	switch {
	case g.App == "":
		return Guide{}, fmt.Errorf("guide %q: no app", id)
	case len(g.Android)+len(g.IOS) == 0:
		return Guide{}, fmt.Errorf("guide %q: no android or ios id", id)
	case g.Body == "":
		return Guide{}, fmt.Errorf("guide %q: no body", id)
	case utf8.RuneCountInString(g.Body) > MaxBody:
		return Guide{}, fmt.Errorf("guide %q: the body has %d characters, more than %d", id, utf8.RuneCountInString(g.Body), MaxBody)
	}
	for _, s := range append(append([]string{}, g.Android...), g.IOS...) {
		if strings.TrimSpace(s) == "" || strings.ContainsAny(s, " /") {
			return Guide{}, fmt.Errorf("guide %q: bad id %q", id, s)
		}
	}
	return g, nil
}

// Text is the guide as the model reads it.
func (g Guide) Text() string {
	return fmt.Sprintf("<app_guide app=%q>\n%s\n</app_guide>", g.App, g.Body)
}

// index is every guide, by app id and by name.
type index struct {
	all    []Guide
	byID   map[string]int
	byName map[string]int
}

var (
	loadOnce sync.Once
	loaded   index
	loadErr  error
)

func load() (index, error) {
	loadOnce.Do(func() { loaded, loadErr = build(files) })
	return loaded, loadErr
}

// build reads every guide of fsys and checks that no app id is in two.
func build(fsys fs.FS) (index, error) {
	x := index{byID: map[string]int{}, byName: map[string]int{}}
	names, err := fs.Glob(fsys, "*.md")
	if err != nil {
		return x, err
	}
	sort.Strings(names)
	var errs []error
	for _, n := range names {
		if n == "CONTRIBUTING.md" || n == "README.md" {
			continue
		}
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		g, err := Parse(strings.TrimSuffix(path.Base(n), ".md"), data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		i := len(x.all)
		x.all = append(x.all, g)
		for _, id := range append(append([]string{}, g.Android...), g.IOS...) {
			if j, ok := x.byID[id]; ok {
				errs = append(errs, fmt.Errorf("guides %q and %q both have %s", x.all[j].ID, g.ID, id))
				continue
			}
			x.byID[id] = i
		}
		for _, name := range append([]string{g.App}, g.Names...) {
			k := strings.ToLower(strings.TrimSpace(name))
			if _, ok := x.byName[k]; !ok && k != "" {
				x.byName[k] = i
			}
		}
	}
	return x, errors.Join(errs...)
}

// All returns every guide, sorted by id.
func All() []Guide {
	x, _ := load()
	return append([]Guide(nil), x.all...)
}

// For returns the guide of an app: by its package name or bundle id, or
// by its name when the phone does not say its id.
func For(id, name string) (Guide, bool) {
	x, _ := load()
	if id = strings.TrimSpace(id); id != "" {
		i, ok := x.byID[id]
		if !ok {
			return Guide{}, false
		}
		return x.all[i], true
	}
	i, ok := x.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Guide{}, false
	}
	return x.all[i], true
}

// Check reads every guide and returns what is wrong with them.
func Check() error {
	_, err := load()
	return err
}
