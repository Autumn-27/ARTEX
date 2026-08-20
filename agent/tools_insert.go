package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"发现新资产时立即批量插入资产到资产库。支持一次提交多种资产类型：root_domain/ip/subdomain/app/service/endpoint。\n"+
			"每条资产的字段说明：\n"+
			"• root_domain：domain(根域名, 必填)、icp(备案, 可选)\n"+
			"• ip：ip(IP地址, 必填)、bound_domains([域名])、open_ports([{port,service}])\n"+
			"• subdomain：domain(子域名, 必填)、record_type(A/AAAA/CNAME等)、record_value(记录值)、icp(可选)\n"+
			"• app：app_name(应用名, 必填)、bundle_id(bundle id, 可选)、category、description、app_icp、company_id(归属企业 id, 可选)\n"+
			"• service(http)：url(必填)、technologies([指纹])、status_code、content_length、page_title、favicon_mmh3、auth([{type,username,password,...}])、service_ip\n"+
			"• service(other)：service_name(必填)、ip或domain(至少一个)、port(必填)、auth([...])\n"+
			"• endpoint：url(必填)、method(必填)、params([{location,name,value,type}])、service_ip\n"+
			"auth/technologies/params 都是【追加合并】(append)，不会覆盖原有值。\n"+
			"返回：{results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id 不暴露给模型：worker 归属哪个 task 由程序经 SetTaskID 权威赋值(见 handler)。
			"assets": map[string]any{
				"type":        "array",
				"description": "资产数组，每个元素对应一条资产记录",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "资产类型",
					},
					// root_domain / subdomain
					"domain":      str("根域名或子域名（root_domain/subdomain 必填）"),
					"icp":         str("ICP 备案号（可选）"),
					"record_type": str("DNS 解析类型：A/AAAA/CNAME/MX 等（subdomain 可选）"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 解析值列表（subdomain 可选，如 [\"1.2.3.4\",\"2.3.4.5\"]）",
					},
					// ip
					"ip": str("IP 地址（ip 类型必填；service/endpoint 类型可填，用于关联 IP）"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "该 IP 绑定的域名列表（ip 类型可选）",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "开放端口列表（ip 类型可选）",
						"items": obj(map[string]any{
							"port":    intp("端口号"),
							"service": str("服务名称，如 http/ssh/mysql 等（可选）"),
						}, "port"),
					},
					// app
					"app_name":    str("应用名称（app 类型必填）"),
					"bundle_id":   str("Bundle ID（app 类型可选）"),
					"category":    str("应用分类（可选）"),
					"description": str("应用描述（可选）"),
					"app_icp":     str("应用 ICP 备案（可选）"),
					"company_id":  intp("归属企业 id（app 类型可选；app 无法靠 scope 自动归因，需显式指定。id 由 add_company_scope 返回）"),
					// service (http)
					"url":         str("完整 URL，含协议和端口（HTTP 服务必填；service_type 自动设为 http）"),
					"status_code": intp("HTTP 响应状态码，如 200/301/403/404（可选）"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 响应体字节数（可选）",
					},
					"page_title":   str("页面 <title> 内容（可选）"),
					"favicon_mmh3": str("favicon MMH3 哈希（可选）"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "指纹/技术栈列表，如 [\"Nginx\",\"Vue\",\"Bootstrap\"]（可选）",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "发现的认证信息列表，每条含 type/username/password 等字段（可选，追加不覆盖）",
						"items":       map[string]any{"type": "object"},
					},
					// service (other，非 HTTP)
					"service_name": str("服务名称，如 ssh/mysql/redis（service 非 HTTP 时必填）"),
					"port":         intp("端口号（service 非 HTTP 时必填）"),
					// endpoint
					"method": str("HTTP 方法：GET/POST/PUT/PATCH/DELETE 等（endpoint 必填）"),
					"params": map[string]any{
						"type":        "array",
						"description": "请求参数列表，每条含 location(query/body/header/path)/name/value/type（可选，追加不覆盖）",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id 由程序权威赋值(worker: SetTaskID)，不接受模型传入——避免模型漏传/错传
			// 导致资产未归任务或归错任务。无任务上下文的调用方(auto/pentest/chat)其 t.taskID=0。
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			for i, item := range a.Assets {
				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				// 自动入测试范围(source='auto')：只对 worker 顶层显式插入的这一项，按其
				// 类型加保守范围；side-effect 派生的资产不经此处，故范围不盲目扩大。taskID=0 时无操作。
				svcIP := item.ServiceIP
				if svcIP == "" {
					svcIP = item.IP
				}
				_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"把根域名/IP/IP段(CIDR)加入某公司的【资产范围】——加入后系统自动认领所有命中的资产(现有+未来)。\n"+
			"公司名唯一：company 不存在则新建，已存在则复用(只把范围并进去)。\n"+
			"scope 一行一条，支持：根域名 / 单个 IP / CIDR 网段(如 1.2.3.0/24)。\n"+
			"务必给 reason 说明归属依据(whois/证书/ASN 等)。\n"+
			"护栏：拒绝裸 TLD 与过宽网段(IPv4<=/16、IPv6<=/32)，非法行会被跳过并在 errors 返回。",
		obj(map[string]any{
			"company": str("公司名(不存在则新建、存在则复用；名称唯一)"),
			"scope":   str("资产范围，一行一条：根域名 / IP / CIDR"),
			"reason":  str("归属依据(证据/来源)，务必填写"),
			"logo":    str("公司图标 URL(可选；仅新建公司时生效)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope 未启用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company 不能为空"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("创建/获取公司失败: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"把测试范围加入【本任务】——这是资产测试覆盖度的分母，也是本任务的授权边界。\n"+
			"kind 支持：company(整个公司名下资产) / root_domain(整个根域，含所有子域) / subdomain(单个精确子域) / ip / cidr。\n"+
			"说明：worker 逐个碰到的主机会被系统【自动】加进范围(精确子域)；本工具用于【主动扩大】——把整个根域/整个公司纳入，或补充指定某子域/IP。\n"+
			"value：company 传公司名或 id(公司须已存在)；root_domain/subdomain 传域名；ip/cidr 传 IP 或网段。\n"+
			"务必给 reason 说明依据(可审计)。多条用 entries 数组。",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "批量：[{kind, value}]。kind∈company/root_domain/subdomain/ip/cidr；value=公司名或id / 域名 / IP / CIDR。", "items": map[string]any{"type": "object"}},
			"kind":    str("[单条] company / root_domain / subdomain / ip / cidr"),
			"value":   str("[单条] 公司名或id / 域名 / IP / CIDR"),
			"reason":  str("加入依据(用于审计)，务必填写"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope 需要任务上下文(当前无 task)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 单条模式
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"查询【本任务及直接关联任务】范围内、还没被事实锚点覆盖的资产（关联范围只读，供你自己判断要不要补测，不代替你决策）。\n"+
			"可选按资产类型过滤：root_domain/subdomain/service/app/endpoint/ip。\n"+
			"分页：page 从 1 起、page_size 默认 10。返回 {assets:[{id,type,label}], total, page, page_size}。仅任务上下文可用。",
		obj(map[string]any{
			"type":      str("资产类型过滤（可选）：root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("页码，从 1 起（默认 1）"),
			"page_size": intp("每页数量（默认 10）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets 需要任务上下文"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"查询资产库。支持 DSL 表达式搜索、按 id/ids 直接取，分页。\n"+
			"DSL 语法：field=value 模糊(ILIKE) | field==value 精确 | field!=value 排除 | port>80 数字比较 | 裸词=全文模糊。\n"+
			"逻辑运算：AND / OR（关键字，AND 优先级高于 OR），括号分组。\n"+
			"资产类型用独立的 type 参数过滤，DSL 里不含 type 字段。\n"+
			"约束：未传 id/ids 时 dsl 必须非空——不允许无条件查询全部资产，必须带查询条件。\n"+
			"字段一览：\n"+
			"  domain      域名（根域名/子域名/服务域名）\n"+
			"  root_domain 根域名（仅子域名/服务资产有）\n"+
			"  ip          IPv4/IPv6 地址\n"+
			"  url         完整 URL（服务/接口）\n"+
			"  page_title  页面标题（HTTP 服务）\n"+
			"  icp         ICP 备案号（根域名）\n"+
			"  service_name 服务名称（非 HTTP 服务）\n"+
			"  app_name    应用名称（app 类型）\n"+
			"  method      HTTP 方法（接口类型，如 GET/POST）\n"+
			"  service_type HTTP 服务类型：http|other\n"+
			"  record_type DNS 解析类型（子域名，如 A/CNAME）\n"+
			"  technology  指纹/技术栈（数组字段，= 模糊 == 精确）\n"+
			"  port        端口号（整数，支持 > >= < <=）\n"+
			"  status_code HTTP 状态码（整数，支持 > >= < <=）\n"+
			"  company_id  归属企业 id（整数）\n"+
			"  task_id     来源任务 id（整数）\n"+
			"示例：status_code>=400 AND technology=shiro\n"+
			"      method==POST AND url=/api/admin\n"+
			"      icp=京 OR icp=沪\n"+
			"      (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 查询表达式，参见工具描述。未传 id/ids 时必须非空。例：status_code>=400 technology=shiro / method==POST url=/api/admin`),
			"type":   str("资产类型过滤：root_domain|ip|subdomain|app|service|endpoint（独立字段，可与 dsl 叠加；单独 type 不足以查询，仍需 dsl）"),
			"id":     intp("直接按单个资产 id 取（可选，与 dsl/type 互斥）"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "直接按多个资产 id 取（可选，与 dsl/type 互斥）"},
			"limit":  intp("返回上限，默认 10（可选）"),
			"offset": intp("分页偏移，默认 0（可选）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDs([]int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDs(a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSL(a.DSL, a.Type, a.Limit, a.Offset)
			default:
				return actool.Errorf("未传 id/ids 时 dsl 不能为空：不允许无条件查询全部资产，请提供查询条件"), nil
			}
			if err != nil {
				return actool.Errorf("DSL 错误: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (企业) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"列出资产库中的【企业/公司】及其资产范围(scope)与已归属资产数。用于查看有哪些公司、"+
			"拿到 company_id（insert_assets 关联 app、list_assets 按 company_id 过滤时用）。"+
			"可选 search 按公司名模糊过滤(不区分大小写)，留空返回全部。",
		obj(map[string]any{
			"search": str("按公司名模糊过滤(可选，不区分大小写)；留空返回全部"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies 未启用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("查询公司失败: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.addFinding(), t.recordFact(),
		// cross-work retrieval
		t.searchAllWorkerTraces(), t.listWorkerTraces(), t.getWorkerTrace(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(), t.listCompanies(),
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// set_goals：人可在运行时给本任务补一个新的最终目标（规划者据此重判是否达成）。
		t.setGoals(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
