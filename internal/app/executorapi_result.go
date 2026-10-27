package app

import "net/http"

func (h *executorAPI) result(w http.ResponseWriter, _ *http.Request) {
	h.fail(w, http.StatusInternalServerError, "internal")
}
