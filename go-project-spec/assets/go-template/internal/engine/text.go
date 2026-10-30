package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// TextStatsEngine 文本解析引擎示例（纯 Go 实现）。
// 每个能力一个文件，文件名即能力名，便于按功能检索与维护。
type TextStatsEngine struct{}

func (TextStatsEngine) Name() string { return "text-stats" }

func (TextStatsEngine) Description() string {
	return "统计文本的字符数、单词数、行数，以及关键词出现次数"
}

type textStatsPayload struct {
	Text    string `json:"text"`
	Keyword string `json:"keyword"`
}

type textStatsOutput struct {
	Chars       int `json:"chars"`
	Words       int `json:"words"`
	Lines       int `json:"lines"`
	KeywordHits int `json:"keyword_hits"`
}

func (TextStatsEngine) Execute(ctx context.Context, payload []byte) (any, error) {
	var p textStatsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("%w: malformed payload: %v", ErrInvalidInput, err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("%w: payload.text is required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := textStatsOutput{
		Chars: utf8.RuneCountInString(p.Text),
		Words: len(strings.Fields(p.Text)),
		Lines: strings.Count(p.Text, "\n") + 1,
	}
	if p.Keyword != "" {
		out.KeywordHits = strings.Count(p.Text, p.Keyword)
	}
	return out, nil
}
