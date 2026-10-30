package handler

import (
	"net/http"
	"strconv"

	"example.com/app/internal/service"
)

// UserHandler 示例业务处理器：只做参数处理 + 调用 service + 统一响应。
type UserHandler struct {
	svc *service.UserService
}

func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	res, err := h.svc.List(r.Context(), page, size, r.URL.Query().Get("keyword"))
	if err != nil {
		mapError(w, err)
		return
	}
	WriteOK(w, res)
}

func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		WriteFail(w, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	u, err := h.svc.Get(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	WriteOK(w, u)
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		WriteFail(w, http.StatusBadRequest, 40000, "invalid json body")
		return
	}
	u, err := h.svc.Create(r.Context(), req.Name)
	if err != nil {
		mapError(w, err)
		return
	}
	WriteOK(w, u)
}
