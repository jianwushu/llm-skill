// Package handler 是 HTTP 处理层：参数绑定/校验 → 调 service/engine → 统一响应格式。
// 禁止直接访问 *sql.DB 或 repo；禁止写业务规则。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"example.com/app/internal/engine"
	"example.com/app/internal/repo"
	"example.com/app/internal/service"
)

// Response 全项目统一响应格式，禁止各 handler 自造格式。
type Response struct {
	Code int    `json:"code"` // 0=成功；非 0 为业务错误码
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

func WriteOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Response{Code: 0, Msg: "ok", Data: data})
}

func WriteFail(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, Response{Code: code, Msg: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody 限制请求体大小并解析 JSON，避免超大请求打爆内存。
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // 8MB
	return json.NewDecoder(r.Body).Decode(dst)
}

// mapError 统一错误映射：领域错误 → HTTP 状态码 + 业务 code；未知错误不泄漏细节。
func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repo.ErrNotFound):
		WriteFail(w, http.StatusNotFound, 40400, "resource not found")
	case errors.Is(err, engine.ErrNotFound):
		WriteFail(w, http.StatusNotFound, 40401, "engine not found")
	case errors.Is(err, engine.ErrBusy):
		WriteFail(w, http.StatusTooManyRequests, 42900, "engine busy, retry later")
	case errors.Is(err, engine.ErrInvalidInput):
		WriteFail(w, http.StatusBadRequest, 40000, err.Error())
	case errors.Is(err, service.ErrInvalidName):
		WriteFail(w, http.StatusBadRequest, 40000, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		WriteFail(w, http.StatusGatewayTimeout, 50400, "execution timeout")
	case errors.Is(err, context.Canceled):
		WriteFail(w, http.StatusRequestTimeout, 40800, "request canceled")
	default:
		slog.Error("internal error", "err", err)
		WriteFail(w, http.StatusInternalServerError, 50000, "internal server error")
	}
}
