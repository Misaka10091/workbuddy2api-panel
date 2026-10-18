package panel

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestAppJSGlobMatchesBackendSemantics 前端预览用的 akGlob 必须与后端 path.Match
// 语义一致，否则弹框预览说"能用"、真正请求却 403 —— 这类不一致比没有预览更糟。
//
// 这里用同一组用例同时喂给 JS 实现，覆盖 * / ? / 大小写 / 空串 / 多通配符。
// 后端侧的等价断言在 internal/apikeys/TestGlobMatch，两处用例保持一致。
func TestAppJSGlobMatchesBackendSemantics(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; glob parity test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function akGlob');
const end = src.indexOf('// akFilterAllows');
if (start < 0 || end < 0) throw new Error('akGlob not found');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.g=akGlob;', ctx);

// 与 internal/apikeys/TestGlobMatch 同一组用例
const cases = [
  ['hy3','hy3',true], ['hy3','hy3x',false], ['hy3','hy',false],
  ['glm-*','glm-5.2',true], ['glm-*','glm-5.3-flash',true],
  ['glm-*','glm-',true], ['glm-*','hy3',false],
  ['*-sol','gpt-6-sol',true], ['*-sol','gpt-6',false],
  ['gpt-*-sol','gpt-6.1-sol',true], ['gpt-*-sol','gpt-6-sol',true],
  ['gpt-*-sol','gpt--sol',true], ['gpt-*-sol','gpt-sol',false],
  ['gpt-*-sol','gpt-6-luna',false],
  ['*','',true], ['*','anything',true],
  ['gpt-?-sol','gpt-6-sol',true], ['gpt-?-sol','gpt-6.1-sol',false],
  ['hy?','hy3',true], ['hy?','hy33',false],
  ['glm-?.?-*','glm-5.3-flash',true], ['glm-?.?-*','glm-5-flash',false],
  ['GLM-*','glm-5.2',true], ['glm-*','GLM-5.2',true],
  ['*-*','a-b',true], ['**','x',true],
  ['a*b*c','aXbYc',true], ['a*b*c','abc',true], ['a*b*c','acb',false],
];
const out = [];
for (const [p, s, want] of cases) {
  const got = ctx.g(p, s);
  if (got !== want) out.push(p + ' vs ' + s + ' => ' + got + ' want ' + want);
}
process.stdout.write(out.length ? out.join(' | ') : 'ALL OK');`
	f, err := os.CreateTemp(t.TempDir(), "glob-parity-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("glob parity node test failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "ALL OK" {
		t.Fatalf("前端 akGlob 与后端语义不一致: %s", got)
	}
}

// TestAppJSFilterAllowsOrder 前端预览的判定顺序必须也是「黑名单优先」。
// 顺序错了最典型的后果：「白名单 glm-* + 黑名单 glm-5.1」预览会显示 5.1 可用，
// 而后端实际拒绝。
func TestAppJSFilterAllowsOrder(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; filter order test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function akGlob');
const end = src.indexOf('// akPreviewModels');
if (start < 0 || end < 0) throw new Error('filter helpers not found');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.f=akFilterAllows;', ctx);

const allow = ['glm-*'];
const deny = ['glm-5.1', '*-flash'];
process.stdout.write(JSON.stringify({
  allowed: ctx.f('glm-5.2', allow, deny),
  deniedExact: ctx.f('glm-5.1', allow, deny),
  deniedWildcard: ctx.f('glm-5.3-flash', allow, deny),
  notInAllow: ctx.f('hy3', allow, deny),
  emptyRules: ctx.f('anything', [], []),
  denyOnly: ctx.f('gpt-6-sol', [], ['gpt-*']),
  realmPrefixed: ctx.f('global:glm-5.2', allow, deny),
}));`
	f, err := os.CreateTemp(t.TempDir(), "filter-order-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("filter order node test failed: %v\n%s", err, out)
	}
	const want = `{"allowed":true,"deniedExact":false,"deniedWildcard":false,` +
		`"notInAllow":false,"emptyRules":true,"denyOnly":false,"realmPrefixed":true}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("前端过滤判定=%s\nwant %s", got, want)
	}
}

// TestAppJSFilterVerdictMatchesAllows 预览表格用的 akFilterVerdict 必须与
// akFilterAllows 判定一致（前者多返回原因）。两处各写一套判定 = 迟早漂移，
// 表现为表格显示"可用"、实际请求被拒。
func TestAppJSFilterVerdictMatchesAllows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; verdict parity test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function akGlob');
const end = src.indexOf('// akPreviewModels');
if (start < 0 || end < 0) throw new Error('filter helpers not found');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.a=akFilterAllows; this.v=akFilterVerdict;', ctx);

const cases = [
  ['glm-5.2', ['glm-*'], ['glm-5.1']],
  ['glm-5.1', ['glm-*'], ['glm-5.1']],
  ['hy3', ['glm-*'], []],
  ['gpt-6-sol', [], ['gpt-*']],
  ['anything', [], []],
  ['glm-5.3-flash', ['glm-*'], ['*-flash']],
];
const diffs = [];
for (const [m, a, d] of cases) {
  if (ctx.a(m, a, d) !== ctx.v(m, a, d).ok) diffs.push(m);
}
// 再验证「原因」字段本身：黑名单命中与白名单缺失要能区分开
process.stdout.write(JSON.stringify({
  diffs,
  denyRule: ctx.v('glm-5.1', ['glm-*'], ['glm-5.1']),
  missing: ctx.v('hy3', ['glm-*'], []),
  allowHit: ctx.v('glm-5.2', ['glm-*'], []),
  noRule: ctx.v('x', [], []),
}));`
	f, err := os.CreateTemp(t.TempDir(), "verdict-parity-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("verdict parity node test failed: %v\n%s", err, out)
	}
	const want = `{"diffs":[],` +
		`"denyRule":{"ok":false,"kind":"deny","rule":"glm-5.1"},` +
		`"missing":{"ok":false,"kind":"missing","rule":""},` +
		`"allowHit":{"ok":true,"kind":"allow","rule":"glm-*"},` +
		`"noRule":{"ok":true,"kind":"","rule":""}}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("akFilterVerdict=%s\nwant %s", got, want)
	}
}

// TestPanelIndexHasApiKeyDialog 新建密钥必须是弹框（不是页内表单），
// 且弹框里要含模型过滤的两个输入与实时预览容器。
func TestPanelIndexHasApiKeyDialog(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, needle := range []string{
		`id="akVeil"`,      // 新建弹框
		`id="akDoneVeil"`,  // 创建后明文弹框
		`id="btnAkNew"`,    // 打开弹框的按钮
		`id="akAllow"`,     // 白名单输入
		`id="akDeny"`,      // 黑名单输入
		`id="akPreview"`,   // 实时预览容器（表格）
		`id="btnAkCreate"`, // 弹框内提交
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("index.html 缺少 %s（密钥弹框/模型过滤界面不完整）", needle)
		}
	}
	// 页内不应再有旧的「新建密钥」表单块（它已被弹框取代）。
	if strings.Contains(body, `<h3>新建密钥 <span class="hint">每把密钥绑定一个域`) {
		t.Error("页面里仍残留旧的页内新建表单")
	}
	// 必须写明「匹配的是模型 ID，不是名称」——这是最容易用错的一点。
	if !strings.Contains(body, "模型 ID") {
		t.Error("界面未说明过滤依据是模型 ID（用户会按展示名称写规则，导致静默不匹配）")
	}
}
