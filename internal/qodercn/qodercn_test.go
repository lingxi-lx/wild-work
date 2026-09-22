package qodercn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"wild-work/internal/auth"
	"wild-work/internal/provider"
)

func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"Qwen3.8-Max":      "qwen3.8-max",
		"DeepSeek-V4-Pro":  "deepseek-v4-pro",
		"GLM-5.3":          "glm-5.3",
		"Kimi-K2.7-Code":   "kimi-k2.7-code",
		"MiniMax-M2.7":     "minimax-m2.7",
		"Auto":             "auto",
		"Qwen3.8 Max Test": "qwen3.8-max-test",
	}
	for in, want := range cases {
		if got := NormalizeModelName(in); got != want {
			t.Errorf("NormalizeModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	plain := []byte(`{"a":1,"b":"中文内容 😀","c":[true,null,3.14]}`)
	enc := qoderEncode(plain)
	if enc == "" {
		t.Fatal("empty encode")
	}
	dec, err := qoderDecode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(dec) != string(plain) {
		t.Fatalf("roundtrip mismatch")
	}
}

// TestParseDynamicModelsScenesFallback 验证 assistant→developer→chat 三级回退。
func TestParseDynamicModelsScenesFallback(t *testing.T) {
	// chat 缺失 → developer 回退
	raw := map[string]json.RawMessage{
		"developer": json.RawMessage(`[{"key":"dk","enable":true,"display_name":"Dev"}]`),
	}
	ms, err := parseDynamicModels(raw)
	if err != nil || len(ms) != 1 || ms[0].Key != "dk" {
		t.Errorf("developer fallback: %v %v", ms, err)
	}
	// 三场景都空 → 错误
	raw = map[string]json.RawMessage{"chat": json.RawMessage(`[]`)}
	if _, err := parseDynamicModels(raw); err == nil {
		t.Errorf("empty scenes should error")
	}
	// assistant 优先于 chat
	raw = map[string]json.RawMessage{
		"chat":      json.RawMessage(`[{"key":"ck","enable":true}]`),
		"assistant": json.RawMessage(`[{"key":"ak","enable":true}]`),
	}
	ms, err = parseDynamicModels(raw)
	if err != nil || len(ms) != 1 || ms[0].Key != "ak" {
		t.Errorf("assistant precedence: %v %v", ms, err)
	}
}

// TestParseDynamicModelsContextWindow 验证解析端真的把 context_config 读进 ModelEntry。
// 回归：此前 ContextWindow 是 json:"-" 且解析只 Unmarshal 到 ModelEntry，
// context_config 被整段丢弃，导致永远回退到 max_input_tokens（恒为 180000 兜底）。
func TestParseDynamicModelsContextWindow(t *testing.T) {
	raw := map[string]json.RawMessage{
		"assistant": json.RawMessage(`[
			{"key":"m1","display_name":"M1","enable":true,"is_default":true,
			 "is_reasoning":true,"is_vl":true,"max_input_tokens":180000,"price_factor":0.5,
			 "context_config":{"default":{"is_default":true,"token_count":1000000},
			                   "compact":{"is_default":false,"token_count":180000}}},
			{"key":"m2","display_name":"M2","enable":true,"max_input_tokens":96000,"price_factor":0.1},
			{"key":"m3","display_name":"M3","enable":true,"max_input_tokens":180000,
			 "context_config":{"c":{"token_count":500000}}},
			{"key":"m4","display_name":"M4","enable":true,"max_input_tokens":180000,
			 "context_config":[{"token_count":7}]},
			{"key":"m5","display_name":"M5","enable":true,"max_input_tokens":180000,
			 "context_config":{"a":{"is_default":true,"token_count":400000},
			                   "b":{"is_default":true,"token_count":272000}}},
			{"key":"off","display_name":"OFF","enable":false,
			 "context_config":{"d":{"is_default":true,"token_count":9}}}
		]`),
	}
	ms, err := parseDynamicModels(raw)
	if err != nil {
		t.Fatalf("parseDynamicModels: %v", err)
	}
	if len(ms) != 5 {
		t.Fatalf("enabled len = %d, want 5 (off 应被过滤)", len(ms))
	}
	// ① is_default 那项胜出，而不是 max_input_tokens 的 180000
	if ms[0].ContextWindow != 1000000 {
		t.Errorf("is_default token_count not parsed: %+v", ms[0])
	}
	// ② 无 context_config → 0，留给 toModelInfos 回退 max_input_tokens
	if ms[1].ContextWindow != 0 {
		t.Errorf("absent context_config should stay 0: %+v", ms[1])
	}
	// ③ **无 is_default 标记时不猜**：返回 0，让上层回退 max_input_tokens。
	// 回归：曾按 label 字典序取最小项，而字典序首位是 "1M"，会取到**最大档**（不安全方向）。
	if ms[2].ContextWindow != 0 {
		t.Errorf("unmarked context_config must not be guessed: %+v", ms[2])
	}
	// ④ 形状不符（数组）只损失本字段，不应让整批模型解析失败
	if ms[3].Key != "m4" || ms[3].ContextWindow != 0 {
		t.Errorf("malformed context_config should degrade to 0: %+v", ms[3])
	}
	// ⑤ 多个档同时标默认 → 取最小值（确定性 + 保守，不依赖 map 迭代序）
	if ms[4].ContextWindow != 272000 {
		t.Errorf("multi-default should take min: %+v", ms[4])
	}
	// ⑥ 一路落到对外展示的 ContextWindow
	infos := toModelInfos(ms)
	if infos[0].ContextWindow != 1000000 || !infos[0].ContextFromAPI {
		t.Errorf("toModelInfos[0]: %+v", infos[0])
	}
	if infos[1].ContextWindow != 96000 || !infos[1].ContextFromAPI {
		t.Errorf("toModelInfos[1] max_input fallback: %+v", infos[1])
	}
	if infos[2].ContextWindow != 180000 {
		t.Errorf("toModelInfos[2] should fall back to max_input: %+v", infos[2])
	}
}

// TestBuildAgentBodyFormatSourceFromUpstream 验证 model_config 的 format/source 走**上游真值**，
// 而非硬编码常量。P0b 实测两渠道 204/204 条目的 format/source 分别为 "openai"/"system"，
// 故改前改后线上行为一致；本测试用非默认值证明真的是“取自上游”而非“恰好写对”。
func TestBuildAgentBodyFormatSourceFromUpstream(t *testing.T) {
	body, err := buildAgentBody(
		[]map[string]any{{"role": "user", "content": "hi"}},
		&ModelEntry{Key: "k1", DisplayName: "D1", Format: "up-format", Source: "up-source"},
		nil, false, 0, "")
	if err != nil {
		t.Fatalf("buildAgentBody: %v", err)
	}
	var parsed struct {
		ModelConfig map[string]any `json:"model_config"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed.ModelConfig["format"] != "up-format" {
		t.Errorf("model_config.format = %v, want upstream value", parsed.ModelConfig["format"])
	}
	if parsed.ModelConfig["source"] != "up-source" {
		t.Errorf("model_config.source = %v, want upstream value", parsed.ModelConfig["source"])
	}

	// 上游未下发时才走兜底常量
	body, err = buildAgentBody([]map[string]any{{"role": "user", "content": "hi"}},
		&ModelEntry{Key: "k2"}, nil, false, 0, "")
	if err != nil {
		t.Fatalf("buildAgentBody(fallback): %v", err)
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body(fallback): %v", err)
	}
	if parsed.ModelConfig["format"] != defaultFormat || parsed.ModelConfig["source"] != defaultSource {
		t.Errorf("empty upstream values should use fallback: %+v", parsed.ModelConfig)
	}
}

// TestModelCacheNoStaticFallback 验证「上次成功缓存」语义：动态拉取失败回退缓存，无缓存报错。
func TestModelCacheNoStaticFallback(t *testing.T) {
	c := New()
	// 无缓存：未命中模型 key 原样返回（无静态兜底）
	if got := c.modelKey("deepseek-v4-pro"); got != "deepseek-v4-pro" {
		t.Errorf("no-static fallback: got %q, want passthrough", got)
	}
	// 写入缓存后命中
	c.setCache([]ModelEntry{{Key: "gmodel", DisplayName: "GLM-5.3"}})
	if got := c.modelKey("glm-5.3"); got != "gmodel" {
		t.Errorf("cache hit: got %q", got)
	}
	if cached := c.cachedModels(); len(cached) != 1 {
		t.Errorf("cachedModels len = %d", len(cached))
	}
}

// TestToModelInfosContextWindow 验证 context_config 优先于 max_input_tokens。
func TestToModelInfosContextWindow(t *testing.T) {
	dyn := []ModelEntry{
		{Key: "m1", DisplayName: "M1", MaxInputTokens: 100000, ContextWindow: 200000},
		{Key: "m2", DisplayName: "M2", MaxInputTokens: 150000},
		{Key: "m3", DisplayName: "M3", IsVL: true, IsReasoning: true},
	}
	infos := toModelInfos(dyn)
	if len(infos) != 3 {
		t.Fatalf("len = %d", len(infos))
	}
	if infos[0].ContextWindow != 200000 || !infos[0].ContextFromAPI {
		t.Errorf("context_config should win: %+v", infos[0])
	}
	if infos[1].ContextWindow != 150000 {
		t.Errorf("max_input fallback: %+v", infos[1])
	}
	if !infos[2].SupportsImages || !infos[2].SupportsReasoning {
		t.Errorf("capabilities: %+v", infos[2])
	}
}

// newAuth 测试凭据（已含机器指纹）。
func newAuth() *auth.Auth {
	return &auth.Auth{
		Kind: "qodercn", AccessToken: "dt-test", RefreshToken: "drt-test",
		UID: "u1", Nickname: "tester",
		MachineID: "mid", MachineToken: "mtok", MachineType: "mtype",
	}
}

// TestCheckinCampaignsClaimable 端到端：campaigns 主路径领取成功。
func TestCheckinCampaignsClaimable(t *testing.T) {
	var claimCalled int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpCampaigns:
			// 校验桌面端签到头（cosy-clienttype=10 是关键）
			if r.Header.Get("cosy-clienttype") != "10" {
				t.Errorf("cosy-clienttype = %q, want 10", r.Header.Get("cosy-clienttype"))
			}
			if r.Header.Get("authorization") != "Bearer dt-test" {
				t.Errorf("authorization = %q", r.Header.Get("authorization"))
			}
			w.Write([]byte(`{"campaigns":[{"campaignId":"c1","campaignKey":"act-20260920-549","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE"}]}`))
		case EpCampaigns + "/c1/claim":
			claimCalled++
			w.Write([]byte(`{"status":"CLAIMED","replayed":false,"benefit":{"amount":100}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldHost := checkinHost
	checkinHost = srv.URL
	defer func() { checkinHost = oldHost }()

	a := newAuth()
	if err := checkin(a); err != nil {
		t.Fatalf("checkin: %v", err)
	}
	if claimCalled != 1 {
		t.Errorf("claim called %d times", claimCalled)
	}
}

// TestCheckinAlreadyClaimed 幂等：409 / CLAIMED / replayed 都算成功。
func TestCheckinAlreadyClaimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpCampaigns:
			w.Write([]byte(`{"campaigns":[{"campaignId":"c1","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMED"}]}`))
		default:
			t.Errorf("unexpected claim on already-claimed: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldHost := checkinHost
	checkinHost = srv.URL
	defer func() { checkinHost = oldHost }()

	if err := checkin(newAuth()); err != nil {
		t.Fatalf("already-claimed should be success: %v", err)
	}
}

// TestCheckinFallbackToDailyCheckin campaigns 不可用（404）时回退 daily-check-in。
func TestCheckinFallbackToDailyCheckin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpCampaigns:
			w.WriteHeader(http.StatusNotFound)
		case EpCheckinSt:
			w.Write([]byte(`{"status":"CLAIMABLE","rewardCredits":100}`))
		case EpCheckinCl:
			w.Write([]byte(`{"success":true,"rewardCredits":100}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldHost := checkinHost
	checkinHost = srv.URL
	defer func() { checkinHost = oldHost }()

	if err := checkin(newAuth()); err != nil {
		t.Fatalf("fallback checkin: %v", err)
	}
}

// TestCheckinDailyDisabled legacy DISABLED：按无活动成功处理。
func TestCheckinDailyDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpCampaigns:
			w.WriteHeader(http.StatusNotFound)
		case EpCheckinSt:
			w.Write([]byte(`{"campaignKey":"cn_daily_check_in_legacy","status":"DISABLED","rewardCredits":100}`))
		default:
			t.Errorf("claim should not fire on DISABLED: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldHost := checkinHost
	checkinHost = srv.URL
	defer func() { checkinHost = oldHost }()

	if err := checkin(newAuth()); err != nil {
		t.Fatalf("DISABLED should be success: %v", err)
	}
}

// TestCheckin401SessionDead 401 透传（供 scheduler 自愈重试）。
func TestCheckin401SessionDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"TOKEN_EXPIRE"}`))
	}))
	defer srv.Close()
	oldHost := checkinHost
	checkinHost = srv.URL
	defer func() { checkinHost = oldHost }()

	if err := checkin(newAuth()); err == nil {
		t.Fatal("401 should propagate error")
	}
}

// TestBuildAgentBodyBodyShape 验证请求体模板形态（session_type=qoder 等差异字段）。
func TestBuildAgentBodyShape(t *testing.T) {
	mc := &ModelEntry{Key: "gmodel", DisplayName: "GLM-5.3", MaxInputTokens: 180000}
	raw, err := buildAgentBody(
		[]map[string]any{{"role": "developer", "content": "sys"}, {"role": "user", "content": "hi"}},
		mc, nil, true, 0, "personal_standard")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["session_type"] != "qoder" {
		t.Errorf("session_type = %v, want qoder", body["session_type"])
	}
	if body["agent_id"] != "agent_common" || body["chat_task"] != "FREE_INPUT" {
		t.Errorf("agent/chat_task = %v/%v", body["agent_id"], body["chat_task"])
	}
	if body["task_id"] != "common" || body["version"] != "3" {
		t.Errorf("qoder2api fields missing: task_id=%v version=%v", body["task_id"], body["version"])
	}
	params, _ := body["parameters"].(map[string]any)
	if params["max_tokens"] != float64(32768) {
		t.Errorf("default max_tokens = %v", params["max_tokens"])
	}
	mcfg, _ := body["model_config"].(map[string]any)
	if mcfg["key"] != "gmodel" || mcfg["is_reasoning"] != true || mcfg["source"] != "system" {
		t.Errorf("model_config = %v", mcfg)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d", len(msgs))
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" { // developer → system
		t.Errorf("developer should be rewritten to system, got %v", m0["role"])
	}
}

// TestClassifyOrder 429 优先于 hardMarkers（不变式 15）。
func TestClassifyOrder(t *testing.T) {
	if got := Classify(http.StatusTooManyRequests, `{"isQuotaExceeded":true}`); got != provider.ErrSoftRate {
		t.Errorf("429 + quota text should be soft rate, got %v", got)
	}
	if got := Classify(402, "x"); got != provider.ErrHardCredit {
		t.Errorf("402 should be hard credit, got %v", got)
	}
	if got := Classify(401, `{"code":"TOKEN_EXPIRE"}`); got != provider.ErrSessionDead {
		t.Errorf("TOKEN_EXPIRE should be session dead, got %v", got)
	}
}
