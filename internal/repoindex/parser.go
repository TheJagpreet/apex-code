package repoindex

import (
	"context"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
)

// ParserRegistry is the parser boundary for Phase 5. It registers the
// priority languages from the plan and keeps extraction outline-first.
type ParserRegistry struct {
	languages map[string]bool
}

func NewParserRegistry() *ParserRegistry {
	return &ParserRegistry{languages: map[string]bool{
		"go": true, "typescript": true, "tsx": true,
		"javascript": true, "jsx": true, "python": true,
	}}
}

func (r *ParserRegistry) ParseFile(ctx context.Context, file WalkedFile) ([]Symbol, error) {
	if !r.languages[file.Language] {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return nil, err
	}
	if file.Language == "go" {
		return parseGoSymbols(file, src)
	}
	return parseLineSymbols(file, string(src)), nil
}

func parseGoSymbols(file WalkedFile, src []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	parsed, err := goparser.ParseFile(fset, file.Path, src, goparser.ParseComments)
	if err != nil {
		return nil, err
	}
	symbols := make([]Symbol, 0)
	for _, decl := range parsed.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := SymbolFunction
			if d.Recv != nil {
				kind = SymbolMethod
			}
			symbols = append(symbols, Symbol{
				FilePath:  file.Path,
				Name:      d.Name.Name,
				Kind:      kind,
				Signature: goSignature(src, fset, d),
				Doc:       commentText(d.Doc),
				StartLine: fset.Position(d.Pos()).Line,
				EndLine:   fset.Position(d.End()).Line,
				Language:  file.Language,
			})
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				symbols = append(symbols, Symbol{
					FilePath:  file.Path,
					Name:      ts.Name.Name,
					Kind:      SymbolType,
					Signature: goDeclLine(src, fset, d.Pos(), ts.End()),
					Doc:       firstNonEmpty(commentText(ts.Doc), commentText(d.Doc)),
					StartLine: fset.Position(ts.Pos()).Line,
					EndLine:   fset.Position(ts.End()).Line,
					Language:  file.Language,
				})
			}
		}
	}
	return symbols, nil
}

func parseLineSymbols(file WalkedFile, src string) []Symbol {
	lines := strings.Split(src, "\n")
	symbols := make([]Symbol, 0)
	for i, line := range lines {
		if sym, ok := lineSymbol(file, lines, i, line); ok {
			symbols = append(symbols, sym)
		}
	}
	return symbols
}

var (
	tsFunction = regexp.MustCompile(`^\s*(export\s+)?(async\s+)?function\s+([A-Za-z_$][\w$]*)`)
	tsClass    = regexp.MustCompile(`^\s*(export\s+)?(abstract\s+)?class\s+([A-Za-z_$][\w$]*)`)
	tsIface    = regexp.MustCompile(`^\s*(export\s+)?interface\s+([A-Za-z_$][\w$]*)`)
	tsType     = regexp.MustCompile(`^\s*(export\s+)?type\s+([A-Za-z_$][\w$]*)`)
	tsArrow    = regexp.MustCompile(`^\s*(export\s+)?(const|let|var)\s+([A-Za-z_$][\w$]*)\s*=.*(=>|function)`)
	pyFunc     = regexp.MustCompile(`^\s*def\s+([A-Za-z_]\w*)\s*\(`)
	pyClass    = regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`)
)

func lineSymbol(file WalkedFile, lines []string, idx int, line string) (Symbol, bool) {
	switch file.Language {
	case "typescript", "tsx", "javascript", "jsx":
		for _, rule := range []struct {
			re   *regexp.Regexp
			kind SymbolKind
			slot int
		}{
			{tsFunction, SymbolFunction, 3},
			{tsClass, SymbolClass, 3},
			{tsIface, SymbolType, 2},
			{tsType, SymbolType, 2},
			{tsArrow, SymbolVariable, 3},
		} {
			if m := rule.re.FindStringSubmatch(line); m != nil {
				return lineBasedSymbol(file, lines, idx, m[rule.slot], rule.kind), true
			}
		}
	case "python":
		if m := pyFunc.FindStringSubmatch(line); m != nil {
			return lineBasedSymbol(file, lines, idx, m[1], SymbolFunction), true
		}
		if m := pyClass.FindStringSubmatch(line); m != nil {
			return lineBasedSymbol(file, lines, idx, m[1], SymbolClass), true
		}
	}
	return Symbol{}, false
}

func lineBasedSymbol(file WalkedFile, lines []string, idx int, name string, kind SymbolKind) Symbol {
	return Symbol{
		FilePath:  file.Path,
		Name:      name,
		Kind:      kind,
		Signature: compactOneLine(lines[idx], 220),
		Doc:       leadingLineDoc(lines, idx),
		StartLine: idx + 1,
		EndLine:   idx + 1,
		Language:  file.Language,
	}
}

func goSignature(src []byte, fset *token.FileSet, d *ast.FuncDecl) string {
	end := d.End()
	if d.Body != nil {
		end = d.Body.Pos()
	}
	return goDeclLine(src, fset, d.Pos(), end)
}

func goDeclLine(src []byte, fset *token.FileSet, start, end token.Pos) string {
	file := fset.File(start)
	if file == nil {
		return ""
	}
	begin := file.Offset(start)
	finish := file.Offset(end)
	if begin < 0 || finish > len(src) || begin >= finish {
		return ""
	}
	return compactOneLine(string(src[begin:finish]), 220)
}

func commentText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return compactOneLine(group.Text(), 180)
}

func leadingLineDoc(lines []string, idx int) string {
	docs := make([]string, 0, 3)
	for i := idx - 1; i >= 0 && len(docs) < 3; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			if len(docs) == 0 {
				continue
			}
			break
		}
		switch {
		case strings.HasPrefix(line, "//"):
			docs = append(docs, strings.TrimSpace(strings.TrimPrefix(line, "//")))
		case strings.HasPrefix(line, "#"):
			docs = append(docs, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		default:
			i = -1
		}
	}
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	return compactOneLine(strings.Join(docs, " "), 180)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
