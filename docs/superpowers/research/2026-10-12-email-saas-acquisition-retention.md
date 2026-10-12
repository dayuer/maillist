# 海外 Email List 产品：获客与留存打法调研

- 调研日期：2026-10-12
- 调研对象：Mailchimp、Klaviyo、Substack、beehiiv、Kit（原 ConvertKit）、MailerLite、Buttondown；第五至七节补充 Ghost、Sendy、listmonk、SparkLoop、EmailOctopus、上游 mailcheap
- 方法：一手来源优先（S-1 招股书、官方产品页/博客、官方定价页、代理商案例、收购公告），关键数字均注明出处；付费墙来源的转引数字单独标注置信度
- 关联：为 Nibwire（本仓库，原名 maillist）的产品/GTM 决策提供参照，启示见第四节，商业模式选择见第五节，托管版发信隔离见第六节，支付意愿验证见第七节，许可策略见第八节

## 一页结论

| 产品 | 获客主打 | 留存主打 |
|---|---|---|
| **Mailchimp** | 免费档（2009 起）+ 内容/SEO + 品牌战役（2017 起） | 品牌心智 + 生态集成；2021 年被 Intuit 约 $12B 收购（现金+股票，bootstrap 起家从未融资） |
| **Klaviyo** | 电商生态绑定（Shopify 持股 11.2%）+ 代理渠道 | 用量计价随客户增长扩张：NRR 119%，36 个月 cohort 扩张至 192% |
| **Substack** | 推荐网络（写作者互推，官方称网络驱动 300 万+ 订阅） | 收入抽成（10%）模式，宣称"无锁定"——名单随时可导出 |
| **beehiiv** | 推荐网络（免费档即含）+ 付费推荐市场（Boosts） | 双边网络锁定：离开即失去分发与广告收入（创作者累计已赚 $37.87M） |
| **Kit (ConvertKit)** | 创始人自有受众 + Creator Network（3000 创作者互推，$1–5/次成功推荐） | 创作者教育生态 + 自动化资产 |
| **MailerLite / Buttondown** | 低价 / 开发者极简定位 | 轻量、锁定弱——靠价格与口碑 |

## 一、获客：六种打法

### 1. 免费档 + 产品内病毒环（Mailchimp 开创，beehiiv 延续）

免费档的本质不是"试用"而是**获客渠道**：免费用户的每封邮件都带 "Sent with Mailchimp" 页脚徽章——收件人里的潜在客户每次收信都在看广告。Mailchimp 2009 年推免费档后一年内用户从约 8.5 万增至 45 万、付费客户增长约 150%（出自 HBR《Making Freemium Work》，原文付费墙，数字被广泛转引，置信中）。beehiiv 今天的免费档（≤2500 订阅者、发送量不限）同样自带品牌页脚，且把推荐网络也放进免费档——免费用户本身就是网络节点。

### 2. 平台生态绑定（Klaviyo 的教科书案例）

据 S-1 原文：77.5% 的 ARR 来自 Shopify 平台上的商家，但**只有 10.6% 的新 ARR 来自 Shopify 应用商店**——大头靠"Shopify 官方推荐位 + 集成成为默认选项"，而非商店自然流量。Shopify 另战略投资 $100M（持股 11.2% + 1570 万份认股权证、7 年协议）。第二渠道是约 5000 家代理/合作伙伴。要点：**绑定一个增长中的宿主平台，让"选邮件工具"这个决策消失**。

### 3. 推荐网络（2022 年后的核心创新）

机制：新订阅者确认页展示"我推荐的 newsletter"，由写作者人工选择、非算法（Substack 官方公告）。数据：Substack 官方称其网络（app + 推荐）驱动 300 万+ 订阅；头部作者 Lenny Rachitsky 称其 78% 订阅来自他人推荐（转引）。beehiiv 更进一步做成**付费市场**：Boosts 按每订阅成本付费让别的 newsletter 推荐你；Kit 的 Creator Network（3000 创作者）里每次成功推荐赚 $1–5。本质：**把"客户想要订阅者增长"这个需求，变成平台自己的获客引擎——客户的增长循环就是产品的病毒循环**。

### 4. 品牌战役（Mailchimp 2017，品类情感化的极端案例）

邮件工具是低情感参与度品类，Mailchimp 却用 Droga5 的 "Did You Mean Mailchimp?" 战役（源自 Serial 播客被截断的 "MailKimp" 梗，衍生 FailChips、SnailPrimp 等 9 个虚构项目网站）拿到 6700 万自然曝光与戛纳网络类全场大奖，当时收入约 $400M。意义：在功能无差异的品类里，**把名字念错这件事变成品牌资产**。规模玩家打法，对新产品不可复制，但说明品类天花板。

### 5. 创始人自有受众（beehiiv / Kit 路线）

beehiiv 团队来自 Morning Brew 增长部门，自己的 newsletter 就是最大获客渠道——"用自己卖的方法增长自己"，产品能力的活广告。Kit 的 Nathan Barry 十年公开写创作者教育内容，年度《State of the Creator Economy》报告（2024 版：76% 创作者有收入，较首期 +15pp）既是内容营销又是数据护城河。

### 6. 价格 / 定位切入（MailerLite、Buttondown）

MailerLite 以"同类功能、更低价格"做转化漏斗；Buttondown 面向开发者做极简与 markdown-first。打法传统，但属于新进入者的现实路径。

## 二、留存：六种机制

### 1. 用量计价与客户成功同向（最强的财务留存）

Klaviyo 按"存的消费者 profile 数 + 发送量"阶梯计价（入门 $35/mo）：**客户名单越长、发送越多 → 付费越多**。S-1 披露 NRR 十个季度稳定在 111–121%，2020Q1 cohort 36 个月后扩张至 192%。注意矛盾一面：其**毛留存仅 88%**——NRR 的漂亮主要靠扩张而非留人，年流失仍有两位数。

### 2. 时间到价值（TTV）压到 30 天内

Klaviyo 在 S-1 定义 KAV（Klaviyo Attributed Value）：2022 年新客约 90% 在一年内产生归因收入，**中位数不到 30 天**就让客户看到"这个工具帮我赚了钱"。留存的第一因是让客户先赚到钱。

### 3. 自动化资产锁定

Klaviyo 的 flows、Kit 的 automations：客户在平台里搭建的自动化工作流越多，迁移成本越高——这部分资产导不走。

### 4. 网络价值锁定 —— 与"反锁定"的战略分歧（两条路线均成立，未作取舍）

- **beehiiv 路线**：推荐网络带来的订阅 + Ad Network 广告收入（CPM/CPC 计费，创作者累计赚 $37.87M，付费订阅 0% 抽成仅 Stripe 手续费）——收入与分发**只存在于网络内**，离开即清零。双边市场是最硬的留存。
- **Substack 路线**：官方强调"无锁定"，名单、内容、付费关系全部可导出。留存不靠锁，靠"网络增长红利 + 10% 抽成下不额外付费即享的基础设施"。

### 5. 数据与送达声誉

发件域名声誉、打开/点击历史、细分标签——换平台要重新 warm-up。全行业共享的隐性切换成本。

### 6. 教育、报告与社区

Kit 的创作者教育 + 年度报告、Mailchimp 的内容营销团队、Klaviyo 的代理认证体系：用"成为客户的经营顾问"留在客户。

## 三、代际演变

- **2009–2016**：freemium + SEO/内容（Mailchimp 定义玩法）
- **2017–2021**：品牌化 + 生态绑定（Mailchimp 品牌战役、Klaviyo×Shopify）
- **2022 至今**：推荐网络成为主战场（Substack 首创、beehiiv 商业化、Kit 跟进）——品类从"工具竞争"进入"网络竞争"：**谁能让客户的订阅者增长，客户就留在谁那**。

## 四、对 Nibwire 项目的启示

1. **成本故事是真实差异点，但要挂到增长上**："$0.10/千封"只打动计算成本的人；beehiiv 们的启示是客户的真实 KPI 是订阅者增长。Nibwire 的归档页（SEO 资产）+ 自有域名追踪 = 客户的获取资产，值得在产品叙事里前置。
2. **推荐网络是可以后置但必须有的东西**：阶段 7 归档完成后，newsletter 间互推是自托管产品最难复制的护城河——但中心化推荐网络与自托管架构存在真实的产品矛盾，建议到阶段 6/7 时单独决策。
3. **MCP 集成是被验证的获客面**：beehiiv 定价页已把 "beehiiv MCP（Claude/ChatGPT/Gemini）" 列为付费档功能——本项目的 MCP 接口（阶段 6）不只是便利性，是**分发渠道**：让 agent 生态替产品获客。
4. **计价结构决定留存曲线**：按订阅者规模计价（而非按功能）让收入随客户成功自然增长。Nibwire 是自托管软件不直接适用，但未来若提供托管服务，应按规模计价。
5. **页脚徽章类病毒环，自托管产品天然没有**（用户部署在自己域名下）——获客应走 Buttondown/开发者路线：开源可见性、comparison SEO 页、CLI 工具链在开发者社区的传播。

## 五、商业模式分析

前四节回答"别人怎么拉客、怎么留客"。本节回答更前置的问题：**这个品类的钱从哪来，Nibwire 能拿哪一份**。

### 5.1 品类利润的来源：按订阅者计价 vs 按发送计成本

平台按**订阅者数量**收费，而它们的边际成本是**发送量**。两者之间的价差就是品类的毛利空间。以 1 万订阅者、每月 4 期（4 万封）为例：

| 方案 | 月成本 | 相对 SES 的倍数 |
|---|---|---|
| SES 按功能计价（à la carte，$0.10/千封） | ≈ $4 | 1× |
| SES Essentials 套餐（$0.16/千封，新账户默认） | ≈ $6.4 | 1.6× |
| EmailOctopus（托管，基于 SES；评测数字有分歧） | ≈ $44.5 | ~11× |
| Buttondown | ≈ $79 | ~20× |
| beehiiv Lite（原 Scale，月付；2026-10-01 起，原价 $109） | $139 | ~35× |
| Kit Creator | ≈ $139 | ~35× |

- 价差买的是：编辑器、送达运维、推荐网络、变现工具、客服。用户觉得"不值"的时刻就是流失时刻——上游作者 Marc Lou 离开 beehiiv 的理由正是"很贵（$400/mo）"，然后自己写了 mailcheap（其 X 帖文，转引，置信中）。
- **自托管产品的商业悖论**：它的卖点是"删掉这 11–35 倍价差"，但删掉价差也就删掉了自己的收入来源。任何自托管路线的商业模式都必须回答"那我收什么钱"。
- **SES 计价已变（2026-07-21）**：2025-06-01 之后开通的 SES 账户默认进入 Essentials 套餐（$0.16/千封，含 Virtual Deliverability Manager），旧的 $0.10 按功能计价需手动切回。上游 README 的"约 $0.10/千封"对新用户默认不成立——部署文档应写明切换步骤，否则成本故事打六折。

### 5.2 收入模型谱系

| 模型 | 代表 | 收入来源 | 规模数据 | 与自托管的兼容性 |
|---|---|---|---|---|
| 按订阅者阶梯订阅 | beehiiv、Kit、Buttondown | 月费随名单增长 | Buttondown 年化 >$1M（创始人自述，2026-09；2025 年收入 +61%） | 不兼容（需托管） |
| 交易抽成 | Substack（10%）、Kit（3.5%+30¢） | 付费订阅流水 | — | 弱：自托管不经手资金流 |
| 网络/市场抽成 | beehiiv Boosts、SparkLoop | 推荐成交的佣金 | SparkLoop 抽 20% + 3.5% 手续费；单次推荐通常 $2–7（官方帮助文档） | 可兼容：作为可选插件接入 |
| 一次性许可 | Sendy | $69/安装（更新是否另收费，来源互相矛盾） | — | 兼容，但开源分支可零成本复制 |
| 开源本体 + 官方托管 | Ghost | Ghost(Pro) 托管费 | 2026-03 年化过 $10M；公开看板约 $11.1M ARR、净流失 2.68%（实时看板，置信中） | **天然兼容** |
| 纯开源、无公司 | listmonk（AGPL）、上游 mailcheap（MIT） | 无；作者以影响力变现 | Marc Lou 2025 年总收入 $1.03M，来自约 15 个收入源（自述） | — |

结论：在自托管这一侧，被验证能做到千万级 ARR 的只有 **Ghost 模式**——软件免费，卖"不想自己运维"。

### 5.3 留存的财务面：品类流失基准

- SMB SaaS 月流失普遍 3–7%；年收入 <$1M、卖给 SMB 的公司中位数 5–7%。
- 一份 2026 行业报告把 "Email & Communication Tools" 列为流失最高的品类：**月流失 8.1%（年化 67%）**，原因是功能同质化（单一来源，置信中低）。
- 对照 Ghost 的净流失 2.68%（含扩张抵消）：开源 + 托管并不意味着高流失——"随时能走"反而降低了用户的进入顾虑。
- 推论：按 8% 月流失算，客户平均寿命约 12.5 个月。在低客单价下，可承受的获客成本非常低——**付费投放走不通，只能靠开源可见性、SEO 与口碑等自然渠道**。这与第四节第 5 条的结论一致。

### 5.4 自托管的隐性成本 = 托管版的价值主张

用户付给平台的钱，很大一部分买的是以下这些麻烦的豁免：

1. **SES 账户级封禁风险**：退信率 ≥5% 进入审查、≥10% 可能暂停发送；投诉率 ≥0.1% 审查、≥0.5% 可能暂停。阈值按整个账户计——1 万人名单单期 10 个投诉即触发审查，一次脏数据导入可停掉整个账户。
2. **开通门槛**：SES 沙箱转正申请、DKIM/SPF/DMARC 配置、域名验证。
3. **运维**：服务器、备份、升级、TLS。

Nibwire 现有设计已覆盖其中一部分：抑制名单、at-most-once 发送（D14）、凭据级日预算（D15）、单 SQLite 文件备份简单。**这些不只是工程细节，而是托管版能收费的东西**，也是与 listmonk/Sendy 的差异点。

### 5.5 Nibwire 候选商业模式评估

| 方案 | 收入潜力 | 与现有架构的契合 | 建设成本 | 主要风险 |
|---|---|---|---|---|
| A. 纯开源 + 作者影响力（上游路线） | 无直接收入 | 完全契合 | 零 | 不构成生意，只构成分发 |
| B. 一次性许可（Sendy 路线） | 低 | 契合 | 低 | 上游 MIT，任何人可免费 fork，许可无护城河 |
| C. 单租户托管（Ghost 路线） | 中高 | **高**：单二进制 + 单 SQLite = 每客户一个容器，无需改造多租户 | 中：开通、计费、监控、备份 | 发信账户隔离（见 5.6） |
| D. 自带 SES + 托管送达监控 | 低中 | 高：CLI/MCP 已有数据出口 | 低中 | ARPU 低，用户群窄 |
| E. 推荐网络抽成 | 中（需规模） | 低：中心化网络与自托管存在矛盾 | 高（自建）/ 低（接入 SparkLoop） | 冷启动；上游已有 fork 内置 SparkLoop，先发优势不在我们 |
| F. Agent 原生分发（MCP） | 不直接产生收入 | 高：阶段 6 已完成 | 已付 | beehiiv 已把 MCP 放进付费档——我们免费提供即是切入点，但会被跟进 |

**建议**：主线走 **C（单租户托管）**，A 与 F 作为获客层（开源本体 + MCP 带来自然流量；许可见第八节），E 以接入 SparkLoop 作为可选插件低成本试水，不自建网络；B 不做。

理由：

1. 第 1 节的非目标（不做多实例、不做多用户、不做外部服务）在 SaaS 视角下是缺陷，在"每客户一个容器"的托管视角下恰好是优点——隔离天然、故障域小、迁出就是交出一个 SQLite 文件。
2. Ghost 证明了"开源 + 托管、不锁定"能做到千万级 ARR 和低流失。
3. 11–35 倍的价差留出了充足的定价空间。

托管版的单位经济与定价测算属于内部资料，不在本公开文档中。

### 5.6 待决问题

1. **发信账户模型**（决定 C 是否可行）：共享一个 SES 账户时，一个客户的投诉会封掉所有客户；按客户拆 AWS 子账户（AWS Organizations）隔离好但开通重。**已调研，结论见第六节**：SES Tenants 使 C 可行。
2. **支付意愿验证**：上游 GitHub 约 677 star（发布数日内），说明存在"受不了平台价格"的人群，但 star ≠ 付费。**已做桌面调研并给出实验设计，见第七节**。
3. **许可策略**：上游 MIT（见 NOTICE），本仓库此前无 LICENSE 文件。**已定，见第八节**：AGPL-3.0-only + 商业授权，控制面闭源，自有产品名（Nibwire）+ 商标政策。
4. **待复核数字**：beehiiv "$20M ARR"仅见于二手转述，未找到一手来源，本文未采用。

## 六、托管版的 SES 发信账户隔离方案

问题：托管版（5.5 方案 C）由我们持有 SES 账户替客户发信。SES 的退信/投诉阈值按**整个账户**执法（5.4），一个客户导入一份脏名单，就可能让所有客户停发。本节比较可行的隔离方案。

### 6.1 方案对比

| 方案 | 隔离强度 | 开通摩擦 | 平台风险 | 成本 |
|---|---|---|---|---|
| ① 共享账户，不隔离 | 无 | 零 | 一个客户可停掉全部客户 | SES 发送费 |
| ② 共享账户 + 每客户一个 configuration set | 仅指标隔离，执法仍在账户级 | 低 | 同 ① | 同 ① |
| ③ 共享账户 + **SES Tenants** | 声誉与执法按租户隔离；租户指标仍计入账户总指标 | 低：API 实时开通 | 低至中（见 6.3） | +$0.005/租户/月 +$0.005/千封 |
| ④ 每客户一个 AWS 子账户（Organizations） | 最强：配额、账单、声誉全分离 | **高**：每个账户、每个区域都要单独申请出沙箱，AWS 人工审核，常无理由拒绝，管理账户无法代办 | 最低 | 账户管理开销 |
| ⑤ 客户自带 SES（BYO，填自己的 key） | 完全分离，风险在客户侧 | 高：客户自己出沙箱、配 DKIM | 零 | 零（客户直付 AWS） |
| ⑥ 委托发送（sending authorization：客户持有身份，平台代发） | **无**：退信/投诉计入代发方（平台）账户 | 中 | 同 ① | 同 ① |

④ 的开通摩擦让它不适合自助式小客户；⑥ 看似把身份留在客户名下，但声誉仍算在平台账户上，没有解决问题。

### 6.2 SES Tenants 要点（2025-08-01 上线）

- **是什么**：账户内的逻辑容器。身份（发信域名）、configuration set、模板、IP 池可关联到租户；发信时带 `TenantName`（API）或 `X-SES-TENANT` 头（SMTP），SES 按租户统计发送量、退信率、投诉率，并校验本次请求用到的资源是否属于该租户。
- **自动暂停**：每个租户挂一个声誉策略——Standard（推荐，仅高影响发现时暂停）、Strict（任何发现都暂停）、None（人工监控；但 AWS 保留对严重问题强制暂停的权利）。暂停只影响该租户，其他租户照常发送；状态变化与声誉发现推送到 EventBridge。自动策略暂停的租户可用 `UpdateReputationEntityCustomerManagedStatus` 自行恢复；被 AWS Trust & Safety 暂停的需走支持工单。
- **租户级抑制名单**：可选开启。不开时，账户级抑制名单是共享的——A 客户的退信地址也会在 B 客户那里被抑制。
- **规模与开通**：默认每账户 10,000 个租户，可申请至 300,000；无需开通申请，SES v2 API 直接可用。按区域生效，不跨区复制。
- **价格**：$0.005/租户/月 + $0.005/千封（Enterprise 套餐含 1,000 个租户），对托管版的单位成本影响可以忽略。

### 6.3 Tenants 解决不了的部分

AWS 实施指南原文明确："Tenant metrics contribute to the overall account-level reputation"。其示例：三个租户各发 1,000 封，其中一个退信 150 封（15%）——该租户被暂停，但**账户级退信率已是 5%，恰好踩到账户审查线**。即：

1. Tenants 把"一个客户停掉全部客户"的概率大幅降低，但**没有降到零**——爆炸半径仍是整个账户。
2. 小平台风险最高：租户少时，单个坏租户占账户总量的比例大，很容易把账户总指标拉过线。
3. 被 Trust & Safety（而非自动策略）处置时，恢复靠工单，不受我们控制。

### 6.4 推荐方案：Tenants 为主，分层兜底

| 客户层 | 方案 | 说明 |
|---|---|---|
| 新客户 / 试用 | ③ Tenants + **Strict** 策略 + 低日预算 | 观察期内任何声誉发现即暂停 |
| 正常付费客户 | ③ Tenants + **Standard** 策略 | 观察期结束后切换 |
| 大名单 / 高价值客户 | ③ + 独立账户分片，或 ④ | 单个客户发送量占比高时，移出共享账户 |
| 对价格极敏感 / 已有 SES 的客户 | ⑤ BYO SES | 托管运行、客户自带 SES，平台零发信风险，可作低价档 |

配套措施（缓解 6.3）：

1. **账户分片**：不把所有租户放进同一个 SES 账户。按每账户若干租户（或按客户层）分片，把账户级爆炸半径限制在一个分片内。分片数受 ④ 的出沙箱摩擦约束，所以是"少量账户 × 大量租户"，而不是一客户一账户。
2. **准入在产品层做**：上游的 confirmed opt-in 默认关（spec §443 沿用）。托管版应对导入名单强制确认流程或导入时做地址校验——退信率的第一道闸门在我们自己的代码里，而不是 SES。
3. **用现有日预算做爬坡**：spec 的凭据级日预算（D15、`send_ledger`）可直接充当新租户的发送量爬坡——前几周限额低，按租户指标逐步放开。这是已实现能力，无需新建。
4. **自己的告警线低于 AWS**：订阅 EventBridge 的租户状态与声誉发现事件，在租户退信率 2%、投诉率 0.05% 时先行暂停并通知客户（第三方指南建议值），不等 AWS 动手。
5. **出沙箱申请要明说是 ESP**：AWS re:Post 的客户审核建议文章表明，平台代客户发信是被支持的用途；申请时写清客户审核、强制 opt-in、租户隔离、退信/投诉自动处理与暂停机制，比模糊申请更易获批。

### 6.5 对代码的影响

- **二进制侧很小**：项目当前依赖的 `aws-sdk-go-v2/service/sesv2` v1.79.1 已包含 `SendEmailInput.TenantName` 与全部租户 API。只需新增一个配置项（如 `SES_TENANT_NAME`），在 `internal/mailer/ses.go` 组装 `SendEmailInput` 时带上，与现有 `SES_CONFIGURATION_SET` 同样处理。
- **凭据收窄**：每个托管实例的 IAM 凭据用 `ses:TenantName` 与 `ses:FromAddress` 条件键限定为只能以自己的租户、自己的域名发信——即使实例被攻破，也冒用不了其他租户。这与 D9（凭据只能由 CLI 签发）同一思路：**实例不持有开通租户的权限**，`CreateTenant`、资源关联、IAM 签发都放在托管控制面，不进二进制。
- **待验证**：租户被暂停时 SES 返回的错误类型。若是 `SendingPausedException`，现有 `PAUSE_ERRORS` 分类（spec §239）会把 campaign 置为暂停，行为正确；若是新错误名，需加入分类。应在接入时用一个 Strict 策略的测试租户实测。

### 6.6 结论

托管版的发信账户问题**有可行解**：共享账户 + SES Tenants + 少量账户分片，新增成本很小，二进制只需加一个配置项。剩余风险（租户指标仍计入账户级）靠产品层准入、日预算爬坡和低于 AWS 的自有告警线来压。5.5 推荐的方案 C 不因发信隔离而受阻。

## 七、支付意愿验证

桌面调研只能回答"有没有付费的先例、价格带在哪、该怎么测"，回答不了"会不会有人付给我们"。本节先整理现有证据，再给出实验设计和决策门槛。

### 7.1 现有证据

**需求信号（上游 mailcheap，截至 2026-10-12）**

- 发布约两天：777 star、191 fork。
- 上游关闭了 issue，作者明确不回答部署问题——想用但不会部署的人目前无处求助。
- 上游仅有的 3 个 PR 全是扩展：接入 SparkLoop、改用 Neon + Resend、更新 SES 定价说明。
- **偏差提醒**：star 和 fork 来自开发者，正是**最可能自己部署、最不可能为托管付费**的人群。这批信号证明"讨厌平台按人头收费"的情绪存在，不证明托管版有付费需求。

**触发事件：beehiiv 2026-10 涨价**

- 2026-10-01 起新用户适用新价；**老用户在 2026-11-01 之后的首次续费时切换**，年付用户到期前锁价。
- 1 万订阅者档：Lite（原 Scale）$109 → $139；Pro（原 Max）$219 → $239；部分档位涨幅 48–52%（不同来源）。免费档仍是 2,500 人。
- r/beehiiv 涨价讨论帖首日超过 80 条评论；有创作者公开表示在重新考虑平台。**但未见任何迁移数量的统计**——目前是情绪，不是行为。
- 含义：**11 月续费季是一个有时限的窗口**，验证实验应赶在这之前上线。

**已被付费的先例（价格锚）**

| 产品 | 卖的是什么 | 价格 | 对我们的意义 |
|---|---|---|---|
| PikaPods 托管 listmonk | 只托管运行，不管送达 | 约 $4.8/月起 | "纯托管"的价格地板 |
| Opsily 托管 listmonk | 托管 + 运维 | $8–11/月，不限订阅者 | 同上 |
| Elestio 托管 listmonk | 独立 VM + 备份/监控 | $11/月起，实际常 $15–25 | 同上 |
| EmailOctopus Connect | 软件 + **客户自带 SES** | Pro $8/月起（年付），SES 另付 | **BYO SES 档的直接对标** |
| EmailOctopus 标准版 | 全托管，自家基于 SES 的发信 | 1 万人约 $44.5/月（评测有分歧，另一来源为 $24） | **全托管档的直接对标** |
| Buttondown | 全托管，开发者向 | 1 万人约 $79/月 | 上沿 |
| Ghost(Pro) | 开源本体的官方托管 | — | "开源 + 托管"能做到千万级 ARR |

**最重要的发现：便宜的 SES 托管位置已经有人占了。** EmailOctopus 2014 年起家时就是"Mailchimp 太贵，用 SES 自己发"，至今十年、自述年收入 $3M+（GetLatka 估 $1.2M，冲突）、无融资。它同时卖全托管和 BYO SES 两档——**正是 5.5 方案 C 与 6.4 中 ⑤ 的组合**。因此：

- "比 beehiiv 便宜"不是差异化，是入场门槛。
- Nibwire 的可辩护差异只剩三项：**开源可迁出**（交出一个 SQLite 文件就走，无锁定）、**agent 原生**（MCP 发信全流程）、**单二进制可自托管**（付费用户随时可降级为自托管，这本身是信任卖点）。
- 验证实验必须测的是"这三项值不值钱"，而不只是"便宜值不值钱"。

### 7.2 价格带（定性）

| 档位 | 对标 | 定位 |
|---|---|---|
| 托管运行 + 客户自带 SES | EmailOctopus Connect；listmonk 托管商 | 与对标同一价位 |
| 全托管（含发信，Tenants 隔离） | EmailOctopus 标准版；Buttondown | 低于两者 |
| 全托管 + 迁移代办 + 独立账户分片 | beehiiv | 明显低于 beehiiv |

具体试价由 E1 / E2 验证后确定，不在本公开文档中列出。

### 7.3 目标人群与假设

| 人群 | 画像 | 可能付费的档 | 风险 |
|---|---|---|---|
| A. 平台涨价难民 | beehiiv/Kit 上 2,500–25,000 订阅者，非技术，月账单 $49–169 | 全托管档 | 最大，但可能直接去 EmailOctopus/Buttondown |
| B. 开发者 / indie hacker | 上游 fork 者，会部署 | 自带 SES 档或不付费 | 最可能自托管，付费意愿最低 |
| C. agent 优先的运营者 | 用 Claude/ChatGPT 操作业务，想让 agent 发信 | 全托管或 BYO | 规模未知，但无直接竞品 |

待验证假设（每条带证伪门槛）：

- **H1 需求**：A 类人群在看到价格表后会留邮箱。冷流量访客→留邮箱 **<2% 即证伪**，≥5% 为强信号。
- **H2 价格**：全托管档的试价落在可接受区间内。Van Westendorp 的可接受区间上沿**低于试价即需下调或砍成本**。
- **H3 付费**：候补名单中有人愿意预付。30 天内预付 **<3% 或绝对数 <10 即证伪**。
- **H4 差异化**：留邮箱的人里，选择理由勾选"无锁定/可迁出"或"agent/MCP"的占比 ≥30%。否则 Nibwire 只是更晚进场的 EmailOctopus。

### 7.4 实验设计（承诺度递增，成本递增）

| 序 | 实验 | 做法 | 指标与门槛 | 依赖 |
|---|---|---|---|---|
| E1 | 价格页 fake door | 落地页列三档价格，点击"开始"进入候补表单（不承诺上线日期）。表单问：当前平台、订阅者数、月账单、续费日、选择理由（多选，含 H4 选项） | 访客→留邮箱：<2% 停、2–5% 继续、≥5% 强信号；各档点击分布 | 无需任何托管代码 |
| E2 | 价格敏感度 | 对候补名单发 Van Westendorp 四问 | 20–30 份出方向，≥100 份才定价；剔除自相矛盾答卷（通常 5–15%） | E1 |
| E3 | 预付 / 创始用户价 | 候补名单分批（不要一次群发）开放"首年创始价"预付，可全额退款 | 30 天内预付 ≥3% 且 ≥10 人为过；<1% 停 | Stripe 链接即可 |
| E4 | 礼宾式迁移 | 为 5–10 个预付用户人工迁移：beehiiv 导出 CSV → CLI 导入 → 手工起容器 + 建 SES 租户 | 迁移耗时、首期发送退信/投诉率、60 天留存 | 现有二进制 + 第六节的租户方案，无需控制面 |

流量来源（与第四节第 5 条一致，全是自然渠道）：X 与 Hacker News（Show HN）发布、r/beehiiv 与 r/Newsletters 涨价讨论帖下的回答、对比 SEO 页（"beehiiv price increase alternative"、"mailcheap hosted"）、上游 mailcheap 的 fork 者（B 类，用于测 BYO 档）。

基准提醒（多为工具厂商博客数字，作方向参考）：

- 冷流量访客→留邮箱 2–5% 常见；暖流量（自有受众）6–12%。
- 候补名单→付费：一次群发 1–4%，分批开放 8–15%；名单放置超过 90 天转化显著下滑。
- 预付/定金：冷流量 0.5–5%，暖流量 3–15%。比留邮箱更能证明真实需求。

### 7.5 时间线与决策规则

| 时间 | 动作 |
|---|---|
| 2026-10 下旬 | 上线 E1 价格页，赶在 beehiiv 11 月续费前 |
| 2026-11 上旬 | 候补名单 ≥30 人时发 E2；同时第一批开放 E3 |
| 2026-11–12 | E4 礼宾迁移 5–10 人，观察 60 天 |
| 2027-01 | 决策 |

决策规则：

- **H1 + H3 通过** → 建托管控制面（开通、计费、租户编排），按 7.2 定价上线。
- **H1 过、H3 不过** → 有兴趣无付费：转向 BYO 档或纯开源（方案 A），不建全托管。
- **H4 不过**（选择理由几乎全是"便宜"）→ 与 EmailOctopus 正面价格战不划算；回到开源 + agent 原生定位，商业化后置。
- **H1 不过** → 托管版不做，项目保持开源工具定位。

### 7.6 结论

现有证据只够证明**情绪存在、窗口存在、价格带清晰**：beehiiv 涨价给了一个 11 月截止的时间窗；EmailOctopus、listmonk 托管商把价格带锚在 $5–45；上游 star 数说明"厌恶按人头收费"的人很多。但 star 来自最不会付费的开发者，迁移只有情绪没有数据，而便宜的 SES 托管位置已被 EmailOctopus 占了十年。**付费意愿目前未经验证**，结论取决于 E1–E3。E1 不需要写任何托管代码，应最先做。

## 八、许可策略

> 本节是商业层面的分析，不构成法律意见；正式发布前的许可文本与商标事宜应请律师过目。

### 8.1 现状（2026-10-12 核实）

1. **仓库已公开，但没有 LICENSE 文件**。GitHub 显示 license 为空；根目录只有 `NOTICE`，内容是上游 mailcheap 的 MIT 声明。也就是说：上游移植来的部分按 MIT 授权，**本仓库新写的代码目前默认"保留所有权利"**——别人可以在 GitHub 上看和 fork，但法律上不能使用、修改或部署。这与第五至七节"开源本体作获客层"的前提矛盾，**无论最终选哪个许可，都应尽快补上**。
2. **105 个提交全部出自同一作者**。现在选任何许可都无需征得他人同意；一旦接受外部贡献，改许可的自由度就开始收窄（见 8.5）。
3. **直接依赖全部是宽松许可**：aws-sdk-go-v2、cobra（Apache-2.0）；go-sdk（MCP）、goldmark（MIT）；bluemonday、modernc.org/sqlite、golang.org/x/text（BSD 类）。与 MIT、Apache-2.0、AGPLv3 均兼容，**依赖不构成选择约束**。
4. **产品名与上游相同**：二进制、Go 模块名都是 `mailcheap`，仓库名原为 `maillist`。上游 MIT 授权的是代码，不是名字——这是许可之外的独立问题（见 8.8）。**已于同日处理：产品、Go 模块、二进制与 GitHub 仓库均改名为 Nibwire / `nibwire`**。

### 8.2 一个前提判断：代码本身不是护城河

本仓库就是证据：上游 1.8 万行 JS 在约两天内被移植成 Go。任何竞争者都可以用同样方式从**上游 MIT 代码**出发，完全绕开我们选的许可。所以：

- 许可能保护的只有"我们在上游之上新增的部分"（单二进制、CLI、MCP、凭据与预算、SES 租户接入）。
- 托管业务真正的护城河在许可之外：品牌、送达运维能力（第六节）、信任（第七节 H4 的"无锁定"）。
- 推论：**为了防竞争而选越严的许可，换来的保护越少、付出的信任代价越大**。

### 8.3 先例

| 项目 | 许可 | 经过 | 启示 |
|---|---|---|---|
| Ghost | MIT + 商标政策 | 竞争托管商可合法托管原版 Ghost；但不能自称官方、不能把改版叫 Ghost、不能用相似名字。年化过 $10M | 宽松许可 + 商标，足以支撑官方托管生意 |
| Plausible | MIT → AGPLv3（2020） | 有公司拿其代码闭源、做成直接竞品出售；改 AGPL 后团队称"竞争者拿代码的问题基本消失"，订阅用户与自托管用户不受影响 | AGPL 挡住闭源托管竞品，且仍是 OSI 开源 |
| listmonk | AGPLv3 | 同品类自托管工具 | 同品类先例 |
| Sentry | BSD → BSL（2019）→ FSL（2023） | FSL：禁止两年内做竞品，两年后自动转 MIT/Apache。非 OSI 开源 | "fair source"是可选项，但不能对外称开源 |
| HashiCorp Terraform | MPL → BSL（2023） | 数周内出现 Linux 基金会托管的 OpenTofu 分叉 | **收紧许可会招致分叉** |
| Redis | BSD → RSAL/SSPL（2024）→ 加回 AGPLv3（2025） | 一周内云厂商支持 Valkey 分叉；改回 AGPL 也没能挽回 | **收紧后再放宽，信任回不来** |

### 8.4 决策经过

1. 初版建议：AGPLv3 本体 + 闭源控制面（理由见 8.2：代码不是护城河；AGPL 可逆、仍是 OSI 开源）。
2. 第一次决定（2026-10-12）：所有者要求"商用须开源 + 禁止基于本代码提供 SaaS"。后一条与 OSI 开源定义不相容，且 "AGPL + 禁止托管附加条款"按 AGPLv3 第 7 节可被接收者移除、无效，因此曾选 BSL 1.1（附加使用授权禁止托管、每版本 4 年后转 AGPLv3）。
3. **最终决定（2026-10-12）：AGPL-3.0-only + 单独的商业授权**，替代 BSL。已落地于 dayuer/nibwire#15（`LICENSE` 为 AGPLv3 全文，另有 `COMMERCIAL-LICENSE.md`）。

放弃 BSL 的取舍：BSL 能禁止他人托管，但对外不能称"开源"、外部贡献更少，且 8.2 已说明禁止托管保护的只是上游之上的新增部分——上游 MIT 代码本来就可以被任何人拿去做 SaaS。AGPL + 商业授权保住"开源"身份，把竞争留在品牌与运营层面，并保留一条收入渠道。

### 8.5 AGPL-3.0-only + 商业授权：允许什么、挡住什么

| 使用方式 | AGPL 下 | 商业授权 |
|---|---|---|
| 原样部署，为自己或本组织发 newsletter（含商业用途） | 允许 | 不需要 |
| 修改后仅本组织内部经网络使用 | 允许 | 不需要 |
| 修改后经网络提供给外部用户（含托管 / SaaS），并向这些用户公开修改后的完整源码 | 允许（第 13 条） | 不需要 |
| 修改后对外提供但**不公开修改** | 不允许 | **需要** |
| 并入闭源产品分发 | 不允许 | **需要** |
| 公司政策禁用 AGPL | — | **需要** |

要点：

- **挡住的是闭源分叉**：任何人改了代码再拿去服务外部用户，都必须公开修改——这正是 Plausible 2020 年遇到并解决的问题。
- **挡不住开源的竞争托管**：竞争者公开源码即可合法托管 Nibwire。这一点与 Ghost 的处境相同；应对方式也相同——靠品牌（8.8 商标政策，竞争者不能叫自己 Nibwire）、送达运维能力（第六节）和"官方"身份竞争，而不是靠许可。
- **选 `-only` 而非 `-or-later`**：不预先接受 FSF 未来版本的条款，版本升级由版权人决定。

### 8.6 商业授权：收入来源与前提

- **卖给谁**：不愿公开修改的代理商 / 托管商、要把能力并入闭源产品的厂商（例如想内嵌 MCP 发信能力的 agent 平台，第七节 C 类）、法务禁用 AGPL 的企业。
- **卖什么**：免除 AGPL 的源码公开义务、可并入闭源产品，可选支持与维护；范围、期限、价格按使用规模商定（`COMMERCIAL-LICENSE.md`）。
- **在商业模式中的位置**：第五节方案 C（单租户托管）仍是主收入；商业授权是补充收入，且把"想做闭源竞品的人"转化为付费客户，而不是对手。
- **前提——版权必须集中在许可方手里**：双授权要求许可方有权以两种许可提供全部代码。`CONTRIBUTING.md` 要求外部贡献以 MIT 入站并 `Signed-off-by`，因此许可方可将贡献同时纳入 AGPL 版与商业版。若改为仅 DCO、贡献以 AGPL 入站，商业授权就无法覆盖这些贡献。
- **上游部分**：移植自 mailcheap 的部分为 MIT，两种授权下都保留其声明（`NOTICE`）。

### 8.7 代价与对前文的影响

1. **对外可以称"开源"**：AGPLv3 是 OSI 认证许可。第五、七节中的"源码公开"措辞恢复为"开源"；第七节 H4 的"开源可迁出"卖点成立。
2. **企业采用摩擦**：部分企业法务禁用 AGPL——这部分需求正好由商业授权承接。
3. **控制面闭源不受影响**：开通、计费、租户编排作为独立程序，经 API 与二进制交互、不与之链接，不受 AGPL 约束（6.5）。托管版对二进制本身的修改须照常开源。
4. **托管服务的 AGPL 义务**：许可方作为版权人运营自家托管服务不受 AGPL 约束；但为一致性与信任，托管版所用的二进制修改应同步发布到公开仓库。
5. **日后调整**：放宽到 MIT 随时可做；收紧（如改回 BSL）会遇到 HashiCorp / Redis 式反弹，且已发布的 AGPL 版本永远可用——应视为不可逆方向。

### 8.8 名字与商标

- Ghost 的做法：代码 MIT，名字与 logo 由商标政策保护——别人可以说"托管原版 Ghost"，但不能把改版叫 Ghost，也不能用相似的名字。官方托管靠"官方"二字取胜。
- 本项目原二进制名 `mailcheap` 沿用上游。上游作者若主张该名字，或其他 fork 也用这个名字，托管版无法建立可区分的品牌；许可文本与商业授权也需要一个确定的产品名。**建议在写 LICENSE 之前定下自有产品名**，二进制与模块名随之更改（`NOTICE` 中继续注明移植自 mailcheap），并参照 Ghost 写一份简短商标政策。AGPL 与商业授权都不授予商标权利，可与商标政策并存——这是 AGPL 下对抗开源竞争托管的主要手段。
- **暂定名 "Mail Turbo AI"（2026-10-12）的初步检索**：已有同品类产品 MailerTurbo（mailerturbo.com，邮件营销自动化 + CRM），另有 turboSMTP。字面接近且同类别，弃用。
- **改选 "Nibwire"（2026-10-12 定）**：nib（笔尖，写作）+ wire（通讯社电讯 / 发送）。候选与淘汰（域名查注册局 RDAP，产品查 GitHub 与网页搜索；**未查商标库**）：

| 候选 | .com | .dev | 同名产品 | 结论 |
|---|---|---|---|---|
| **Nibwire** | 未注册 | 未注册 | 未发现 | **选定** |
| Letterwren | 未注册 | 未注册 | 未发现 | 备选 |
| Quillwren / Sendquill / Postwren | 已注册 | 未注册 | 未发现 | 备选 |
| Letterwire | 已注册 | 未注册 | **letterwire.app 是 newsletter 阅读 app**，同品类 | 淘汰 |
| Larkwire | 已注册 | 未注册 | 鸟鸣学习 app（2011 起）；GitHub 上同名 AI agent 工具 | 淘汰 |
| Epistly | 已注册 | 未注册 | 与 Epistle（向订阅者推送更新的 app）过近 | 淘汰 |
| Owlery | 全部已注册 | — | "Owl Post" 涉华纳商标 | 淘汰 |

- 下一步：在 USPTO / EUIPO / CNIPA 的第 9、42 类检索 "Nibwire"，并尽早注册 nibwire.com / nibwire.dev。
- 商标注册的地域、类别、时机不在本文范围，需法务意见。

### 8.9 行动清单

| 优先级 | 动作 | 时机 |
|---|---|---|
| 1 | 定自有产品名 | 已定 Nibwire；商标检索与域名注册待办 |
| 2 | `LICENSE` 改为 AGPL-3.0-only 全文；新增 `COMMERCIAL-LICENSE.md`；`NOTICE` 保留上游 MIT 声明 | 已提交 dayuer/nibwire#15；商业授权合同模板待律师起草 |
| 3 | 写 `CONTRIBUTING.md`：外部贡献入站 MIT + `Signed-off-by`（双授权的前提） | 已随 dayuer/nibwire#15 提交 |
| 4 | README 附许可对照表：AGPL 下能做什么、何时需要商业授权 | README 已随 dayuer/nibwire#15 提交；对外文案待写 |
| 5 | 改二进制与模块名；写商标政策 | dayuer/nibwire#17 暂缓：与阶段 7/8 冲突，待阶段 8 合并后基于最新 main 重做；商标政策待写 |
| 6 | 托管控制面建为独立仓库、独立程序（闭源），只经 API 与二进制交互 | 决定建托管版时（第七节决策规则通过后） |

## 主要来源

- [Klaviyo S-1（SEC 原文）](https://www.sec.gov/Archives/edgar/data/1835830/000162828023030618/klaviyoincs-1.htm)
- [Meritech：Klaviyo S-1 拆解](https://meritech.substack.com/p/klaviyo-ipo-s-1-breakdown)
- [Substack：Recommendations 官方公告](https://on.substack.com/p/recommendations)
- [beehiiv：Ad Network 功能页](https://www.beehiiv.com/features/ad-network)
- [beehiiv：定价页](https://www.beehiiv.com/pricing)
- [Kit：创作者变现与 Creator Network](https://kit.com/resources/blog/creator-monetization)
- [Droga5 × Mailchimp 案例页](https://droga5.com/work/mailchimp)
- [Intuit 收购 Mailchimp 公告](https://www.intuit.com/blog/news/intuit-to-acquire-mailchimp/)
- [HBR：Making Freemium Work](https://hbr.org/2014/05/making-freemium-work)（付费墙，数字转引）
- [Kit：State of the Creator Economy 2024](https://kit.com/resources/blog/email-marketing-stats)
- [Amazon SES 官方定价页](https://aws.amazon.com/ses/pricing/)（2026-07-21 套餐改版）
- [AWS Builder：SES 定价套餐变化解读](https://builder.aws.com/content/3Gubvy72jmeLSzktz28x7ZwYrEh/amazon-ses-pricing-plans-are-here-what-actually-changes-for-your-sending-architecture)
- [SES 退信/投诉阈值说明（第三方整理）](https://www.mailblast.io/blog/ses/amazon-ses-deliverability)
- [beehiiv 2026 定价拆解](https://priceworld.com/blog/beehiiv-pricing-2026/)
- [Kit 2026 定价](https://www.emailvendorselection.com/kit-pricing/)
- [Buttondown 2026 定价](https://yoursolopreneurkit.com/blog/buttondown-pricing-2026)
- [Indie Hackers：Buttondown 达到 $1M ARR 访谈](https://www.indiehackers.com/post/tech/growing-a-side-project-in-a-crowded-market-to-1m-arr-by-playing-the-long-game-eL15KZvm2ax20BtiM5i8)
- [Buttondown 2025 年度回顾](https://buttondown.com/blog/2025)
- [Ghost：About（非营利 + 托管模式）](https://ghost.org/about/)
- [John O'Nolan：Ghost 过 $10M ARR](https://x.com/JohnONolan/status/2029195753428758756)
- [SparkLoop 功能与定价指南](https://help.sparkloop.app/en/articles/9184574-the-ultimate-guide-to-sparkloop-features-pricing)
- [Sendy vs listmonk 对比](https://www.sequenzy.com/versus/sendy-vs-listmonk)
- [上游 marclou/mailcheap](https://github.com/marclou/mailcheap)
- [SaaS 流失基准 2026（按品类）](https://focus-digital.co/average-churn-rate-by-industry-saas/)
- [B2B SaaS 流失基准（939 家公司）](https://optif.ai/learn/questions/b2b-saas-churn-rate-benchmark/)
- [AWS What's New：SES 租户隔离与自动声誉策略（2025-08-01）](https://aws.amazon.com/about-aws/whats-new/2025/08/amazon-ses-tenant-isolation-automated-reputation-policies/)
- [SES 开发者指南：Tenants](https://docs.aws.amazon.com/ses/latest/dg/tenants.html)
- [AWS 博客：Implement Tenants, Part 2（评估与规划）](https://aws.amazon.com/blogs/messaging-and-targeting/implement-tenants-in-your-amazon-ses-environment-part-2-assessment-and-planning/)
- [AWS 博客：Implement Tenants, Part 3（实施指南，含账户级指标示例）](https://aws.amazon.com/blogs/messaging-and-targeting/implement-tenants-in-your-amazon-ses-environment-part-3-implementation-guide/)
- [AWS 博客：按租户隔离抑制名单](https://aws.amazon.com/blogs/messaging-and-targeting/isolate-email-suppression-per-tenant-with-amazon-ses/)
- [SES 开发者指南：出沙箱申请（按账户、按区域）](https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html)
- [SES 开发者指南：Sending authorization 概述](https://docs.aws.amazon.com/ses/latest/dg/sending-authorization-overview.html)
- [AWS re:Post：多客户 SES 平台的客户审核最佳实践](https://repost.aws/articles/ARiPfzfFD3RbGzRPjn-2YdEg/client-vetting-best-practices-for-amazon-ses-customers-managing-multiple-client-accounts)
- [EmailOctopus：beehiiv 2026 涨价与替代方案（竞品撰写，注意立场）](https://emailoctopus.com/blog/beehiiv-price-increase-2026)
- [Net Influencer：beehiiv 涨价遭创作者反弹](https://www.netinfluencer.com/beehiiv-raises-prices-faces-pushback-from-creators/)
- [EmailTooltester：beehiiv 定价（含涨幅）](https://www.emailtooltester.com/en/reviews/beehiiv/pricing/)
- [EmailTooltester：EmailOctopus 定价](https://www.emailtooltester.com/en/reviews/emailoctopus/pricing/)
- [EmailOctopus：营销打法与 $3M+ ARR](https://emailoctopus.com/blog/the-marketing-tactics-that-took-emailoctopus-to-over-3m-in-annual-revenue)
- [Bootstrappers：EmailOctopus 从 SES 脚本起家](https://bootstrappers.com/how-these-founders-went-from-giving-away-their-product-to-making-130000-in-mrr/)
- [Elestio：托管 listmonk](https://elest.io/open-source/listmonk)
- [Opsily：listmonk 托管成本](https://opsily.com/hosting/listmonk/listmonk-cost)
- [PikaPods 应用目录](https://www.pikapods.com/apps/)
- [LaunchList：候补名单转付费基准](https://getlaunchlist.com/blog/convert-waitlist-to-paying-customers)
- [Getwaitlist：候补名单转化基准](https://getwaitlist.com/blog/waitlist-benchmarks-conversion-rates)
- [Fake door 测试门槛](https://swetrix.com/blog/fake-door-test)
- [Van Westendorp 样本量（SaaS 创始人指南）](https://thesaaslibrary.com/pricing-research-methods-saas-founders/)
- [Plausible：为什么从 MIT 改为 AGPL（2020）](https://plausible.io/blog/open-source-licenses)
- [Plausible：BSL 讨论与改 AGPL 后的效果](https://github.com/plausible/analytics/discussions/1195)
- [Ghost 许可说明](https://docs.ghost.org/license/)
- [Ghost 商标政策 v2.0](https://ghost.org/trademark/)
- [FSL（Functional Source License）](https://fsl.software/)
- [Fair Source 许可列表](https://fair.io/licenses/)
- [2026 年许可变更回顾：HashiCorp、Redis、Valkey、OpenTofu](https://www.flowverify.co/blog/open-source-relicensing-2026-what-happened)
- [Business Source License 1.1 原文（MariaDB）](https://mariadb.com/bsl11/)
- [GNU AGPLv3 原文（第 7 节 further restrictions）](https://www.gnu.org/licenses/agpl-3.0.txt)
