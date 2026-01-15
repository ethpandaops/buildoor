package handlers

import (
	"net/http"

	"github.com/ethpandaops/buildoor/pkg/webui/server"
)

type IndexPage struct {
}

// Index will return the "index" page using a go template
func (fh *FrontendHandler) Index(w http.ResponseWriter, r *http.Request) {
	var templateFiles = append(server.LayoutTemplateFiles,
		"index/index.html",
	)

	var pageTemplate = server.GetTemplate(templateFiles...)
	data := server.InitPageData(r, "index", "/", "Buildoor Dashboard", templateFiles)

	var pageError error
	data.Data, pageError = fh.getIndexPageData()
	if pageError != nil {
		server.HandlePageError(w, r, pageError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if server.HandleTemplateError(w, r, "index.go", "Index", "", pageTemplate.ExecuteTemplate(w, "layout", data)) != nil {
		return
	}
}

func (fh *FrontendHandler) getIndexPageData() (*IndexPage, error) {

	return &IndexPage{}, nil
}
