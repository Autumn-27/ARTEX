package chainskel

import (
	"strconv"
	"strings"
)

// verifier 反证 checklist(chains L4 → verifier 判定层):按漏洞类别从骨架库"判据"
// 列蒸馏出的代码常量,由 server 组 verifier 上下文快照时整段注入,让 verifier
// 逐条反证而不是凭印象下 verified/false_positive。
//
// 归一化思路与 db.NormalizeVulnClass 一致(折叠后别名/关键词命中),但本包自持
// 映射:chainskel 是底层包,反过来依赖 db 会造成分层倒置/依赖循环。

// verifierClassRule 把折叠后的 vulnclass 文本映射到专项判据组。按表顺序首个
// 命中生效,所以更具体的关键词(sql/xss/ssrf)排在宽泛词(注入/泄露)前面,
// 且 sqli 用 "sql" 而非 "注入" 作锚,避免吃掉"命令注入"。
var verifierClassRules = []struct {
	class    string
	keywords []string
}{
	{"cmdi", []string{"命令注入", "命令执行", "代码执行", "commandinjection", "cmdi", "rce"}}, // 先于 sqli:防止"命令注入"被"注入"类词截胡
	{"sqli", []string{"sql"}},
	{"xss", []string{"xss", "跨站"}},
	{"ssrf", []string{"ssrf"}},
	{"upload", []string{"文件上传", "上传", "upload", "webshell"}},
	{"auth-bypass", []string{"越权", "未授权", "认证绕过", "鉴权", "idor", "bypass", "逻辑漏洞"}},
	{"info-leak", []string{"信息泄露", "敏感信息", "泄露", "leak", "disclosure"}},
}

// verifierGeneralChecks 是通用反证层:任何类别(含未识别类别)都逐条过。
var verifierGeneralChecks = []string{
	"200 ≠ 打通:WAF/拦截页会伪造 200。先做良性/恶意对照,只有差异化响应才算信号;无对照的 200 不构成证据。",
	"锚定预期特征:结论必须绑定一个只有攻击成功才会出现的特征(独有数据/线性延时/回连/文件落地),响应\"看起来正常\"或\"没报错\"都不构成证据。",
	"阴性 ≠ 无洞:复现失败先排除环境/防护/时机因素再下 false_positive;无法复现又无法排除时给 inconclusive,不要硬判。",
}

// verifierClassChecks 是按类别的专项反证判据(从对应骨架"判据"列蒸馏)。
var verifierClassChecks = map[string][]string{
	"sqli": {
		"真假分叉是核心判据:布尔盲注要同一请求在真/假条件下有可复现的稳定差异;单次报错只说明语法进了 parser,不证明可控。",
		"时间盲注须做基线对照:先测无 payload 的响应耗时基线,延时须显著超出基线且重复 ≥2 次稳定出现。",
		"报错回显须落到可控输出:能让自己构造的值(如 version()、字面量)出现在响应里才算成立。",
	},
	"xss": {
		"回显 ≠ 可执行:确认 payload 回显所在的上下文(HTML body/属性/JS 字符串/URL),且关键字符未被编码/转义,才算成立。",
		"存储型须验证读取方触发:写入后由独立会话/页面重新读取,确认落地且按预期渲染。",
		"反射型在无回显、仅扫描器特征命中时不得判 verified。",
	},
	"ssrf": {
		"回连须确认源 IP 是目标服务端出口(与目标公网 IP/出口段对照),排除 CDN 节点、浏览器端渲染造成的伪 SSRF。",
		"只证明出网不等于 SSRF 影响成立:须进一步验证能触达内网/元数据/改状态接口,或明确标注影响面后定级。",
	},
	"cmdi": {
		"sleep 延时须与设定值线性对应(3s→3s、7s→7s)才排除网络抖动误报;单次延时、非线性延时不可判。",
		"回显/带外证据优先于时间盲:有回连或命令输出回显时以之为准,时间盲只作降级手段。",
	},
	"upload": {
		"落地+解析双验证:文件须确认落盘路径可直接访问,且访问时按脚本被解析执行;访问返回源码/直接下载即未解析,不算成立。",
		"随机名/路径无回显时,未确认落地点与解析行为前不得判 verified。",
	},
	"auth-bypass": {
		"双账号对照是硬判据:B 会话访问到 A 预先埋入唯一标记的私有资源才算越权成立;公开数据/公开接口的\"越权\"是误报。",
		"读与写分别验证:能读不代表能改;仅读到公开/共享数据不得判 verified。",
	},
	"info-leak": {
		"数据真实性优先:样本须含目标独有的真实数据(能关联到目标资产/用户),默认页、测试数据、公开信息不构成泄露。",
		"敏感度定级:仅路径/版本号/banner 类低敏信息不得据此判高危。",
	},
}

// normalizeVerifierClass 折叠 vulnclass(小写、去空白/连字符/下划线)后按
// verifierClassRules 首个命中返回专项判据组名;不命中返回 ""。
func normalizeVerifierClass(v string) string {
	key := strings.ToLower(v)
	for _, r := range []string{" ", "\t", "　", "-", "_", "/", "、", ":", ":"} {
		key = strings.ReplaceAll(key, r, "")
	}
	if key == "" {
		return ""
	}
	for _, rule := range verifierClassRules {
		for _, kw := range rule.keywords {
			if strings.Contains(key, kw) {
				return rule.class
			}
		}
	}
	return ""
}

// VerifierChecklist 按 vulnclass 生成 verifier 反证 checklist:通用层永远带,
// 命中类别时追加该类的专项判据。返回可直接注入上下文的固定文本段。
func VerifierChecklist(vulnclass string) string {
	var b strings.Builder
	b.WriteString("【反证判定 checklist(平台内置;以下判据用于反证,逐条过)】\n通用判据(任何类别都过):\n")
	for i, c := range verifierGeneralChecks {
		b.WriteString(strconv.Itoa(i+1) + ". " + c + "\n")
	}
	if class := normalizeVerifierClass(vulnclass); class != "" {
		b.WriteString("专项判据(vulnclass=\"" + strings.TrimSpace(vulnclass) + "\" → " + class + "):\n")
		for i, c := range verifierClassChecks[class] {
			b.WriteString(strconv.Itoa(i+1) + ". " + c + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
