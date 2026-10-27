package app

import "net/http"

func (h *executorAPI) archive(w http.ResponseWriter, _ *http.Request) {
	h.fail(w, http.StatusInternalServerError, "internal")
}

func (h *executorAPI) file(w http.ResponseWriter, _ *http.Request) {
	h.fail(w, http.StatusInternalServerError, "internal")
}
