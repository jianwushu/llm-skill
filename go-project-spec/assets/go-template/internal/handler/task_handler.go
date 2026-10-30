package handler

import (
	"net/http"

	"example.com/app/internal/engine"
)

// TaskHandler 工具任务入口：按引擎名分发，不感知具体引擎实现。
// 新增能力只需在装配处 Register 引擎，本文件无需修改。
type TaskHandler struct {
	dispatcher *engine.Dispatcher
}

func NewTaskHandler(d *engine.Dispatcher) *TaskHandler {
	return &TaskHandler{dispatcher: d}
}

// Engines 能力发现：GET /api/v1/engines
func (h *TaskHandler) Engines(w http.ResponseWriter, r *http.Request) {
	WriteOK(w, h.dispatcher.List())
}

// Submit 提交任务：POST /api/v1/tasks
//
//	{"engine":"text-stats","payload":{"text":"...","keyword":"..."}}
func (h *TaskHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var req engine.Request
	if err := decodeBody(w, r, &req); err != nil {
		WriteFail(w, http.StatusBadRequest, 40000, "invalid json body")
		return
	}
	if req.Engine == "" {
		WriteFail(w, http.StatusBadRequest, 40000, "engine is required")
		return
	}
	res, err := h.dispatcher.Run(r.Context(), req)
	if err != nil {
		mapError(w, err)
		return
	}
	WriteOK(w, res)
}
