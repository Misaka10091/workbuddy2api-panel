package panel

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestAppJSRealmLabelAndFilter 域标签与账号池按域筛选的纯函数行为。
//
// 「国内版 / 国际版分开管理」的前端核心是一个谓词：accRealm 为空时不做任何过滤
// （历史行为逐字不变），非空时只留该域。这里连 case 一起钉住，防止有人把
// 「空串 = 全池」改成「空串 = 空集」——那会让账号池默认空白。
func TestAppJSRealmLabelAndFilter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; realm filter test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
// 只取需要的两个函数体，不执行整份 app.js（顶层会碰 DOM/fetch）。
const start = src.indexOf('function realmLabel');
const end = src.indexOf('function renderRealmCards');
if (start < 0 || end < 0) throw new Error('realm helpers not found');
const ctx = { String };
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) +
  '\nthis.realmLabel=realmLabel; this.accFiltered=accFiltered; this.setRealm=r=>{accRealm=r};', ctx);

const list = [
  { uid: 'a', realm: 'cn' },
  { uid: 'b', realm: 'global' },
  { uid: 'c' },            // 旧数据无 realm → 按 cn
  { uid: 'd', realm: '' }, // 空串同样按 cn（与后端 Status.Realm 缺省回落同向）
];
const pick = r => { ctx.setRealm(r); return ctx.accFiltered(list).map(x => x.uid); };
process.stdout.write(JSON.stringify({
  labelCn: ctx.realmLabel('cn'),
  labelGlobal: ctx.realmLabel('global'),
  labelAll: ctx.realmLabel(''),
  labelUnknown: ctx.realmLabel('foo'),
  all: pick(''),
  cn: pick('cn'),
  global: pick('global'),
  junk: pick('not-a-realm'),
}));`
	f, err := os.CreateTemp(t.TempDir(), "realm-filter-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("realm filter node test failed: %v\n%s", err, out)
	}
	const want = `{"labelCn":"国内版","labelGlobal":"国际版","labelAll":"全部",` +
		`"labelUnknown":"全部","all":["a","b","c","d"],"cn":["a","c","d"],` +
		`"global":["b"],"junk":["a","b","c","d"]}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("realm filter=%s\nwant %s", got, want)
	}
}

// TestPanelIndexHasRealmCards 面板必须同时具备：域卡片容器、域切换条、
// 三个域的切换按钮。缺一个，「国内版 / 国际版分开管理」在界面上就不成立
// （卡片没容器 = 看不到各域统计；切换条没按钮 = 筛不了）。
func TestPanelIndexHasRealmCards(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, needle := range []string{
		`id="realmCards"`,     // 域统计卡片容器
		`id="accRealmSwitch"`, // 域切换条
		`data-realm="cn"`,     // 国内版选项
		`data-realm="global"`, // 国际版选项
		`id="accRealmCnN"`,    // 切换条上的国内版账号数徽标
		`id="accRealmGlN"`,    // 切换条上的国际版账号数徽标
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("index.html 缺少 %s（分域管理的界面骨架不完整）", needle)
		}
	}
}

// TestAppJSBatchRealmQuery 批量动作的 URL 拼接：全池不带参数（旧行为零回归），
// 具体域带 ?realm=。
func TestAppJSBatchRealmQuery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; batch realm query test skipped")
	}
	src, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function realmQuery');
const end = src.indexOf('// runBatchAction');
if (start < 0 || end < 0) throw new Error('realmQuery not found');
const ctx = { encodeURIComponent };
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.q=realmQuery;', ctx);
process.stdout.write(JSON.stringify({
  all: ctx.q(''),
  cn: ctx.q('cn'),
  global: ctx.q('global'),
}));`
	f, err := os.CreateTemp(t.TempDir(), "realm-query-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("batch realm query node test failed: %v\n%s", err, out)
	}
	const want = `{"all":"","cn":"?realm=cn","global":"?realm=global"}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("realmQuery=%s\nwant %s", got, want)
	}
	// 关键契约：全池必须**不带** realm 参数——后端 batchRealm 据此判定全池，
	// 多送一个 ?realm= 空值虽然也回落全池，但会让审计日志误报「指定了域」。
	if !strings.Contains(string(src), "return realm ? '?realm=' + encodeURIComponent(realm) : '';") {
		t.Error("realmQuery 的全池分支不再返回空串（会把「全部」误报成指定域）")
	}
}
