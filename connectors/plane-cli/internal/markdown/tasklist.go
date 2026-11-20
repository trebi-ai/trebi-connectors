package markdown

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// planeTaskList writes a GFM bullet list in which each item is a task in the
// form the Plane editor reads: <ul data-type="taskList"><li data-type="taskItem"
// data-checked="true">. Other lists keep the GFM <input> checkbox.
type planeTaskList struct{}

func (planeTaskList) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithASTTransformers(util.Prioritized(planeTaskList{}, 0)))
	// Lower value wins; the GFM task list renderer uses 500.
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(planeTaskList{}, 0)))
}

func (planeTaskList) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		list, ok := n.(*ast.List)
		if !entering || !ok || list.IsOrdered() {
			return ast.WalkContinue, nil
		}
		var boxes []*extast.TaskCheckBox
		for item := list.FirstChild(); item != nil; item = item.NextSibling() {
			box := checkBox(item)
			if box == nil {
				return ast.WalkContinue, nil
			}
			boxes = append(boxes, box)
		}
		list.SetAttributeString("data-type", []byte("taskList"))
		for i, item := 0, list.FirstChild(); item != nil; i, item = i+1, item.NextSibling() {
			checked := "false"
			if boxes[i].IsChecked {
				checked = "true"
			}
			item.SetAttributeString("data-type", []byte("taskItem"))
			item.SetAttributeString("data-checked", []byte(checked))
		}
		return ast.WalkContinue, nil
	})
}

// checkBox returns the task checkbox that starts a list item, or nil.
func checkBox(item ast.Node) *extast.TaskCheckBox {
	if item.FirstChild() == nil {
		return nil
	}
	box, _ := item.FirstChild().FirstChild().(*extast.TaskCheckBox)
	return box
}

func (planeTaskList) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(extast.KindTaskCheckBox, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, plane := n.Parent().Parent().AttributeString("data-type"); plane {
			return ast.WalkContinue, nil
		}
		if n.(*extast.TaskCheckBox).IsChecked {
			_, _ = w.WriteString(`<input checked="" disabled="" type="checkbox"> `)
		} else {
			_, _ = w.WriteString(`<input disabled="" type="checkbox"> `)
		}
		return ast.WalkContinue, nil
	})
}
