// tool_enum_type_test.go tools schema 的 enum 补类型（上游 49f9db4 第 1 项）。
//
// 背景：Kimi 系对参数 schema 严格校验，缺 type 的 enum 直接 400 11133「Invalid request
// parameters」——同一个模型对带这类 schema 的客户端等于全废。归一只能**无损推断**：四种
// 单一标量类型补上，混合标量 / 含 null 保持原样（让上游按原语义报错，不猜）。
//
// 三条不可越界的约束：enum 取值一字不改；只有 type 缺失时才补（显式写了的不动）；
// response_format 不走这条（入口只有 obj["tools"]，且输出约束补错类型会改语义）。
package upstream

import (
	"encoding/json"
	"reflect"
	"testing"
)

// toolParamsEnumSchema 构造 tools[].function.parameters 里一个带 enum 的属性 schema。
func toolParamsEnumSchema(enumJSON string) string {
	return `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"type":"function","function":{"name":"pick","parameters":{"type":"object",` +
		`"properties":{"k":{"enum":` + enumJSON + `}}}}}]}`
}

// assertEnumType 断言出站 body 里 tools[0].function.parameters.properties.k.type 的值。
// wantType 为空表示**不该出现 type 键**。
func assertEnumType(t *testing.T, out []byte, wantType string, wantEnum []any) {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}
	tools, ok := obj["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools 结构异常: %#v", obj["tools"])
	}
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	k := params["properties"].(map[string]any)["k"].(map[string]any)
	got, has := k["type"]
	if wantType == "" {
		if has {
			t.Errorf("不该补 type，得到 %v: %s", got, out)
		}
	} else if !has || got != wantType {
		t.Errorf("type=%v want %q: %s", got, wantType, out)
	}
	enum, ok := k["enum"].([]any)
	if !ok {
		t.Fatalf("enum 丢失: %#v", k["enum"])
	}
	// enum 取值一字不改：长度与逐项值都必须原样。
	if len(enum) != len(wantEnum) {
		t.Fatalf("enum 长度被改: got %d want %d (%#v)", len(enum), len(wantEnum), enum)
	}
	for i := range wantEnum {
		// reflect.DeepEqual 而非 ==：enum 里可能有对象/数组（不可比较类型）。
		if !reflect.DeepEqual(enum[i], wantEnum[i]) {
			t.Errorf("enum[%d]=%v want %v（取值不得改写）", i, enum[i], wantEnum[i])
		}
	}
}

// TestToolSchemaEnumTypeInferred 四类单一标量都要能推断：全串 / 全整数 / 含小数 / 全布尔。
// 判定顺序 integer 先于 number（数字解出来一律 float64，靠「值是否整」区分）。
func TestToolSchemaEnumTypeInferred(t *testing.T) {
	cases := []struct {
		name     string
		enumJSON string
		wantType string
		wantEnum []any
	}{
		{"全串", `["a","b"]`, "string", []any{"a", "b"}},
		{"全整数", `[1,2]`, "integer", []any{float64(1), float64(2)}},
		{"含小数", `[1,2.5]`, "number", []any{float64(1), float64(2.5)}},
		{"全布尔", `[true,false]`, "boolean", []any{true, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PrepareBodyOpt([]byte(toolParamsEnumSchema(c.enumJSON)), false)
			assertEnumType(t, out, c.wantType, c.wantEnum)
		})
	}
}

// TestToolSchemaEnumTypeNotInferred 推不出唯一类型时保持原样：混合标量 / 含 null /
// 含对象数组都**不允许**猜一个类型出来（猜错等于改语义，上游应按原样报错）。
func TestToolSchemaEnumTypeNotInferred(t *testing.T) {
	cases := []struct {
		name     string
		enumJSON string
		wantEnum []any
	}{
		{"混合标量", `["a",1]`, []any{"a", float64(1)}},
		{"含 null", `[1,null]`, []any{float64(1), nil}},
		{"含对象", `[{"x":1}]`, []any{map[string]any{"x": float64(1)}}},
		{"空 enum", `[]`, []any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PrepareBodyOpt([]byte(toolParamsEnumSchema(c.enumJSON)), false)
			assertEnumType(t, out, "", c.wantEnum)
		})
	}
}

// TestToolSchemaEnumTypeKeepsExplicitType 显式写了 type 的不动（哪怕与 enum 不匹配）：
// 归一器的职责是「补齐缺失」，不是「纠正客户端」。
func TestToolSchemaEnumTypeKeepsExplicitType(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[],"tools":[{"type":"function",` +
		`"function":{"name":"pick","parameters":{"type":"object","properties":` +
		`{"k":{"type":"string","enum":["p","q"]}}}}}]}`)
	out := PrepareBodyOpt(body, false)
	assertEnumType(t, out, "string", []any{"p", "q"})
}

// TestToolSchemaEnumTypeNested 递归必须覆盖 items / 数组形态与 patternProperties 之后的
// 层级：插在 `patternProperties` 处理之后、递归之前，深层 enum 也照样补齐。
func TestToolSchemaEnumTypeNested(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[],"tools":[{"type":"function",` +
		`"function":{"name":"pick","parameters":{"type":"object","properties":` +
		`{"arr":{"type":"array","items":{"enum":["x","y"]}},` +
		`"any":{"anyOf":[{"enum":[1,2]}]}}}}}]}`)
	out := PrepareBodyOpt(body, false)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}
	params := obj["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)

	items := props["arr"].(map[string]any)["items"].(map[string]any)
	if got := items["type"]; got != "string" {
		t.Errorf("items.enum 应补 string，得 %v", got)
	}
	anySchema := props["any"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	if got := anySchema["type"]; got != "integer" {
		t.Errorf("anyOf[0].enum 应补 integer，得 %v", got)
	}
}

// TestToolSchemaEnumTypeLeavesResponseFormat response_format 不走这条：它是**输出约束**，
// 补错类型会改变模型被约束的方式；且入口只有 obj["tools"]，天然覆盖不到它。
// 断言 response_format 里既不多出 type，也不出现被改写后的 ${k} 形态。
func TestToolSchemaEnumTypeLeavesResponseFormat(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[],"response_format":{"type":"json_schema",` +
		`"json_schema":{"name":"out","schema":{"type":"object","properties":` +
		`{"k":{"enum":["a"]}}}}}}`)
	out := string(PrepareBodyOpt(body, false))
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}
	props := obj["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	k := props["k"].(map[string]any)
	if _, has := k["type"]; has {
		t.Errorf("response_format 的 enum 不得补 type: %s", out)
	}
	if enum, ok := k["enum"].([]any); !ok || len(enum) != 1 || enum[0] != "a" {
		t.Errorf("response_format 的 enum 被改动: %#v", k["enum"])
	}
}

// TestInferEnumType 直接钉住推断函数本身（不经 JSON 管线）：整数值与小数分开，
// 且 integer 先于 number 判定。
//
// 注：空集合不在本函数的契约内——`len(values)==0` 时 `strs == n` 恒真会返回 "string"，
// 这是上游原样实现（逐字照搬，不改），由**调用点**的 `len(e) > 0` 守卫兜住；空 enum
// 不出 type 的行为在 TestToolSchemaEnumTypeNotInferred/空_enum 里从管线上断言。
func TestInferEnumType(t *testing.T) {
	if got := inferEnumType([]any{float64(3)}); got != "integer" {
		t.Errorf("单个整数应推断 integer，得 %q", got)
	}
	if got := inferEnumType([]any{float64(3.5)}); got != "number" {
		t.Errorf("单个小数应推断 number，得 %q", got)
	}
	if got := inferEnumType([]any{"x"}); got != "string" {
		t.Errorf("单个字符串应推断 string，得 %q", got)
	}
}
