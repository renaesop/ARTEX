package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/artex/submission"
	"github.com/Autumn-27/norma/llm"
)

func submissionDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 180000)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, "请求格式无效或超过大小限制")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeErr(w, 400, "请求必须包含且仅包含一个 JSON 对象")
		return false
	}
	return true
}
func submissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, submission.ErrConflict):
		writeErr(w, 409, err.Error())
	case db.SubmissionNotFound(err):
		writeErr(w, 404, "漏洞、厂商模板或草稿不存在")
	default:
		writeErr(w, 500, "提交材料操作失败，请重试")
	}
}
func (s *Server) submissionProfiles(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	profiles, err := pg.SubmissionProfiles(r.Context())
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": profiles})
}
func (s *Server) saveSubmissionProfile(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var p submission.Profile
	if !submissionDecode(w, r, &p) {
		return
	}
	if err := submission.ValidateProfile(p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p, err := pg.SaveSubmissionProfile(r.Context(), p)
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) prepareSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if id, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err != nil || id <= 0 {
		writeErr(w, 400, "无效的漏洞 ID")
		return
	}
	var req struct {
		ProfileID string `json:"profile_id"`
	}
	if !submissionDecode(w, r, &req) {
		return
	}
	draft, err := pg.PrepareSubmission(r.Context(), r.PathValue("id"), req.ProfileID)
	if err != nil {
		submissionError(w, err)
		return
	}
	view, err := pg.SubmissionView(r.Context(), draft.ID)
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) getSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	view, err := pg.SubmissionView(r.Context(), r.PathValue("id"))
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) editSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Revision int64             `json:"revision"`
		Title    string            `json:"title"`
		Body     string            `json:"body"`
		Impact   string            `json:"impact"`
		Values   map[string]string `json:"values"`
	}
	if !submissionDecode(w, r, &req) {
		return
	}
	errContent := submission.ValidateContent(submission.Draft{Title: req.Title, Body: req.Body, Impact: req.Impact})
	if errContent != nil {
		writeErr(w, 400, errContent.Error())
		return
	}
	for _, v := range req.Values {
		if len(v) > 12000 {
			writeErr(w, 400, "字段内容不能超过 12 KB")
			return
		}
	}
	view, err := pg.EditSubmission(r.Context(), r.PathValue("id"), req.Revision, func(d *submission.Draft, _ submission.Source, _ submission.Profile) error {
		for key := range req.Values {
			found := false
			for _, f := range d.Fields {
				if key == f.ID {
					found = true
				}
			}
			if !found {
				return submission.ErrConflict
			}
		}
		d.Title = req.Title
		d.Body = req.Body
		d.Impact = req.Impact
		for i := range d.Fields {
			if value, ok := req.Values[d.Fields[i].ID]; ok && value != d.Fields[i].Value {
				d.Fields[i].Value = value
				d.Fields[i].Sources = []string{"manual"}
			}
		}
		d.State = "draft"
		d.ReviewedAt = nil
		submission.Sanitize(d)
		return nil
	})
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) submissionProvider(id int64) (llm.Provider, agent.Config, bool) {
	if id > 0 {
		return s.providerForProfile(id)
	}
	// Pin the active provider rather than the failover pool: a report must not
	// silently be sent to a different model/vendor (or a more expensive route).
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.llmDirect, s.llmCfg, s.llmDirect != nil
}
func submissionCompletion(ctx context.Context, p llm.Provider, cfg agent.Config, input string) (string, error) {
	limit := 4096
	if cfg.MaxTokens > 0 && cfg.MaxTokens < limit {
		limit = cfg.MaxTokens
	}
	req := llm.CompletionRequest{System: []string{submission.SystemPrompt}, Messages: []llm.Message{llm.UserText(input)}, MaxTokens: limit}
	if !cfg.Stream {
		message, stop, _, err := p.Complete(ctx, req)
		if err != nil {
			return "", err
		}
		if stop == "max_tokens" || stop == "length" {
			return "", errors.New("模型输出达到长度限制")
		}
		if len(message.Text()) > 140000 {
			return "", errors.New("模型输出过长")
		}
		return message.Text(), nil
	}
	var out strings.Builder
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return "", err
		}
		if ev.Type == llm.SETextDelta {
			if out.Len()+len(ev.Text) > 140000 {
				return "", errors.New("模型输出过长")
			}
			out.WriteString(ev.Text)
		}
	}
	return out.String(), nil
}
func (s *Server) generateSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Revision   int64 `json:"revision"`
		ProfileID  int64 `json:"model_profile_id"`
		AllowModel bool  `json:"allow_model"`
	}
	if !submissionDecode(w, r, &req) {
		return
	}
	if !req.AllowModel {
		writeErr(w, 400, "请确认可将当前规则与脱敏证据交给所选模型处理")
		return
	}
	if req.ProfileID < 0 {
		writeErr(w, 400, "无效的模型配置")
		return
	}
	// Bound model work globally. Parallel tabs cannot multiply expensive calls.
	if !s.submissionBusy.CompareAndSwap(false, true) {
		writeErr(w, 429, "已有报告正在生成，请稍后再试")
		return
	}
	defer s.submissionBusy.Store(false)
	base, err := pg.FreshSubmissionBase(r.Context(), r.PathValue("id"))
	if err != nil {
		submissionError(w, err)
		return
	}
	if base.Revision != req.Revision {
		submissionError(w, submission.ErrConflict)
		return
	}
	p, cfg, ok := s.submissionProvider(req.ProfileID)
	if !ok {
		writeErr(w, 400, "请先配置或选择可用的 LLM")
		return
	}
	safeProfile := base.Profile
	safeProfile.Name = submission.Redact(safeProfile.Name)
	safeProfile.Rules = submission.Redact(safeProfile.Rules)
	safeProfile.Destination = ""
	safeProfile.SourceURL = ""
	safeProfile.Fields = append([]submission.Field{}, base.Profile.Fields...)
	for i := range safeProfile.Fields {
		safeProfile.Fields[i].Instruction = submission.Redact(safeProfile.Fields[i].Instruction)
		safeProfile.Fields[i].Label = submission.Redact(safeProfile.Fields[i].Label)
	}
	input, _ := json.Marshal(map[string]any{"program": safeProfile, "source": base.Source})
	if len(input) > 110000 {
		writeErr(w, 400, "规则和证据合计超过 110 KB，请精简漏洞报告或手工编辑草稿")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 80*time.Second)
	defer cancel()
	ctx = llmrec.WithWorker(ctx, "submission_draft")
	text, err := submissionCompletion(ctx, p, cfg, string(input))
	if err != nil {
		writeErr(w, 502, "模型生成失败或超时，原草稿已保留，请稍后重试")
		return
	}
	generated, err := submission.ApplyModel(base, text)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	generated.Model = cfg.Model
	view, err := pg.EditSubmission(r.Context(), base.ID, req.Revision, func(d *submission.Draft, src submission.Source, profile submission.Profile) error {
		if src.Hash() != base.SourceHash || profile.Revision != base.Profile.Revision {
			return submission.ErrConflict
		}
		*d = generated
		return nil
	})
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) confirmSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Revision int64 `json:"revision"`
		Reviewed bool  `json:"reviewed"`
	}
	if !submissionDecode(w, r, &req) {
		return
	}
	if !req.Reviewed {
		writeErr(w, 400, "请先核对复现结果、项目范围和报告内容")
		return
	}
	blocked := false
	view, err := pg.EditSubmission(r.Context(), r.PathValue("id"), req.Revision, func(d *submission.Draft, src submission.Source, p submission.Profile) error {
		if submission.HasBlocking(submission.Checks(*d, src, p)) {
			blocked = true
			return submission.ErrConflict
		}
		now := time.Now().UTC()
		d.State = "ready"
		d.ReviewedAt = &now
		return nil
	})
	if blocked {
		writeErr(w, 409, "草稿检查未通过，请刷新并补齐材料")
		return
	}
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) resetSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Revision int64 `json:"revision"`
	}
	if !submissionDecode(w, r, &req) {
		return
	}
	view, err := pg.EditSubmission(r.Context(), r.PathValue("id"), req.Revision, func(d *submission.Draft, src submission.Source, p submission.Profile) error {
		fresh := submission.NewDraft(src, p)
		fresh.ID = d.ID
		fresh.Revision = d.Revision
		*d = fresh
		return nil
	})
	if err != nil {
		submissionError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) exportSubmission(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	view, err := pg.SubmissionView(r.Context(), r.PathValue("id"))
	if err != nil {
		submissionError(w, err)
		return
	}
	// The browser must name the exact reviewed revision; an old tab must never
	// download a different report after a concurrent edit/confirmation.
	rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || rev != view.Draft.Revision || view.Draft.State != "ready" || submission.HasBlocking(view.Checks) {
		writeErr(w, 409, "报告已变化或尚未确认，请重新检查")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"filename": fmt.Sprintf("finding-%s-disclosure.md", view.Draft.FindingID), "markdown": submission.Markdown(view.Draft), "destination": view.Draft.Profile.Destination})
}
