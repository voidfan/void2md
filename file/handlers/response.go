package handlers

import (
	"encoding/json"
	"net/http"
)

// JSONNoEscape returns JSON response without HTML escaping.
// This prevents response bloat for large HTML tables by not escaping <, >, & characters.
func JSONNoEscape(w http.ResponseWriter, code int, obj interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(obj)
}
