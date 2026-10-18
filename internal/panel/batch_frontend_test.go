package panel

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestPanelIndexHasBatchSelectionUI 账号表必须有多选 UI：选择列、全选、批量条。
//
// 缺任一项，「多选批量操作」在界面上就不成立：
// 没选择列 = 选不了；没全选 = 逐个点；没批量条 = 选了没地方执行。
func TestPanelIndexHasBatchSelectionUI(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, needle := range []string{
		`id="accSelAll"`,     // 表头全选
		`id="accBatchBar"`,   // 批量操作条
		`id="accBatchCount"`, // 已选计数
		`id="btnAccClearSel"`, // 清空选择
		`data-batch="checkin"`,
		`data-batch="balance"`,
		`data-batch="keepalive"`,
		`data-batch="disable"`,
		`data-batch="remove"`,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("index.html 缺少 %s（多选批量操作界面不完整）", needle)
		}
	}
	// 选择列必须与健康色条（th.mark）分开：复用 mark 会吃掉 3px 色条的位置。
	if !strings.Contains(body, `<th class="pick">`) {
		t.Error("表头缺少独立的选择列 th.pick（不能复用 th.mark）")
	}
}

// TestAppJSBatchSelectionPureFns 选择集合的纯函数：只对**可见**账号生效。
//
// 这条是批量操作最危险的一处：被域筛选藏起来的账号如果也参与批量，
// 用户以为"只动了看到的 3 个"，实际动了 7 个（含另一域的号）——后果不可逆。
func TestAppJSBatchSelectionPureFns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; batch selection test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('let accSel = new Set()');
const end = src.indexOf('// syncBatchBar');
if (start < 0 || end < 0) throw new Error('selection helpers not found');
const ctx = { Set, Map, String, Array };
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) +
  '\nthis.sel=()=>accSel; this.prune=pruneAccSel; this.visible=accSelVisible; this.s8=short8;', ctx);

const mk = (uid, realm) => ({ uid, realm });
// 池里 5 个号：3 个 cn、2 个 global
const all = [mk('c1','cn'), mk('c2','cn'), mk('c3','cn'), mk('g1','global'), mk('g2','global')];

// 全选 5 个，但"可见列表"只有 cn 的 3 个
for (const s of all) ctx.sel().add(s.uid);
const cnVisible = all.filter(s => s.realm === 'cn');
const visiblePicked = ctx.visible(cnVisible);

// 裁剪：只把 c1 与 g2 视为存活 → c2/c3/g1 都应被剔除。
ctx.prune([mk('c1','cn'), mk('g2','global')]);

process.stdout.write(JSON.stringify({
  visiblePicked,             // 只应含 c1,c2,c3
  afterPrune: [...ctx.sel()], // 只剩 c1,g2（prune 按"存活列表"裁剪）
  short8: ctx.s8('894c78ef-a8b1-4475'),
}));`
	f, err := os.CreateTemp(t.TempDir(), "batch-sel-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("batch selection node test failed: %v\n%s", err, out)
	}
	const want = `{"visiblePicked":["c1","c2","c3"],` +
		`"afterPrune":["c1","g2"],` +
		`"short8":"894c78ef"}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("batch selection=%s\nwant %s", got, want)
	}
}

// TestAppJSBatchMetaCoversBackendActions 前端动作表必须覆盖后端支持的全部动作。
//
// 只差一个的后果：后端能做的动作在界面上点不到（或点了发不出正确 action）。
// 反方向（前端有、后端无）由后端 unknown action 400 兜住，不必在这里测。
func TestAppJSBatchMetaCoversBackendActions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; batch meta test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('const BATCH_META');
const end = src.indexOf('// runAccountBatch');
if (start < 0 || end < 0) throw new Error('BATCH_META not found');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.m=BATCH_META;', ctx);
process.stdout.write(JSON.stringify(Object.keys(ctx.m).sort()));`
	f, err := os.CreateTemp(t.TempDir(), "batch-meta-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("batch meta node test failed: %v\n%s", err, out)
	}
	var frontend []string
	if err := json.Unmarshal(out, &frontend); err != nil {
		t.Fatalf("parse: %v (%s)", err, out)
	}
	backend := batchActionNames()
	// 后端有、前端无 → 界面上点不到该动作
	fe := map[string]bool{}
	for _, n := range frontend {
		fe[n] = true
	}
	for _, n := range backend {
		if !fe[n] {
			t.Errorf("后端动作 %q 在前端 BATCH_META 里缺失（界面上无法触发）", n)
		}
	}
}
