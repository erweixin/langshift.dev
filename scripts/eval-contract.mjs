const t=(id,sourceEn,sourceZh,targetEn,targetZh,transferEn,transferZh,gapsEn,gapsZh,evidenceEn,evidenceZh)=>({id,source:{en:sourceEn,"zh-CN":sourceZh},target:{en:targetEn,"zh-CN":targetZh},transfer:{en:transferEn,"zh-CN":transferZh},gaps:{en:gapsEn,"zh-CN":gapsZh},evidence:{en:evidenceZh===undefined?evidenceEn[0]:evidenceEn,"zh-CN":evidenceZh===undefined?evidenceEn[1]:evidenceZh}});

export const transitionCatalog=[
  t("frontend_to_ai_application_engineer","Frontend Developer","前端工程师","AI Application Engineer","AI 应用工程师",["component abstraction","asynchronous UI state"],["组件抽象","异步界面状态"],["LLM evaluation","tool orchestration"],["LLM 评测","工具编排"],["shipped a streaming dashboard","交付过流式数据看板"]),
  t("designer_to_product_designer","Visual Designer","视觉设计师","Product Designer","产品设计师",["visual hierarchy","interaction critique"],["视觉层级","交互评审"],["problem discovery","experiment measurement"],["问题发现","实验度量"],["redesigned a checkout flow","重新设计过结账流程"]),
  t("backend_to_ai_infra_engineer","Backend Developer","后端工程师","AI Infrastructure Engineer","AI 基础设施工程师",["distributed systems","service reliability"],["分布式系统","服务可靠性"],["model serving","GPU capacity planning"],["模型服务","GPU 容量规划"],["operated a high-throughput queue","运维过高吞吐队列"]),
  t("student_to_software_engineer","Computer Science Student","计算机专业学生","Software Engineer","软件工程师",["algorithmic reasoning","course project delivery"],["算法推理","课程项目交付"],["production debugging","team change management"],["生产调试","团队变更管理"],["built and tested a compiler project","构建并测试过编译器项目"]),
  t("operator_to_ai_product_operator","Operations Specialist","运营专员","AI Product Operator","AI 产品运营",["workflow analysis","customer feedback synthesis"],["流程分析","客户反馈归纳"],["prompt evaluation","automation safety"],["提示词评测","自动化安全"],["documented and improved a support workflow","记录并改进过客服流程"]),
  t("qa_to_sdet","QA Engineer","测试工程师","Software Development Engineer in Test","测试开发工程师",["failure reproduction","test case design"],["故障复现","测试用例设计"],["test infrastructure coding","property-based testing"],["测试基础设施编码","基于性质的测试"],["maintained a regression suite","维护过回归测试套件"]),
  t("data_analyst_to_analytics_engineer","Data Analyst","数据分析师","Analytics Engineer","分析工程师",["metric definition","SQL analysis"],["指标定义","SQL 分析"],["data modeling","pipeline observability"],["数据建模","管道可观测性"],["owned an executive metrics dashboard","负责过管理指标看板"]),
  t("mobile_to_multimodal_engineer","Mobile Developer","移动端工程师","Multimodal Application Engineer","多模态应用工程师",["device interaction design","latency optimization"],["设备交互设计","延迟优化"],["vision-language evaluation","media preprocessing"],["视觉语言评测","媒体预处理"],["shipped an offline camera workflow","交付过离线相机工作流"]),
  t("devops_to_platform_engineer","DevOps Engineer","DevOps 工程师","Platform Engineer","平台工程师",["deployment automation","incident response"],["部署自动化","故障响应"],["developer platform product thinking","multi-tenant policy"],["开发者平台产品思维","多租户策略"],["operated Kubernetes release pipelines","运维过 Kubernetes 发布流水线"]),
  t("support_to_customer_engineer","Technical Support Specialist","技术支持专员","Customer Engineer","客户工程师",["issue diagnosis","customer communication"],["问题诊断","客户沟通"],["solution architecture","reusable technical demos"],["解决方案架构","可复用技术演示"],["resolved a complex integration incident","解决过复杂集成故障"]),
  t("writer_to_ai_content_strategist","Technical Writer","技术写作者","AI Content Strategist","AI 内容策略师",["audience modeling","structured explanation"],["受众建模","结构化解释"],["content experimentation","AI provenance policy"],["内容实验","AI 来源治理"],["published a developer onboarding guide","发布过开发者入门指南"]),
  t("researcher_to_applied_ai_engineer","Research Scientist","研究科学家","Applied AI Engineer","应用 AI 工程师",["experimental design","literature evaluation"],["实验设计","文献评估"],["production integration","cost and latency tradeoffs"],["生产集成","成本延迟权衡"],["reproduced a published model result","复现过已发表模型结果"]),
  t("pm_to_ai_product_manager","Product Manager","产品经理","AI Product Manager","AI 产品经理",["problem prioritization","cross-functional delivery"],["问题优先级","跨职能交付"],["AI evaluation design","uncertainty communication"],["AI 评测设计","不确定性沟通"],["launched a workflow product","发布过工作流产品"]),
  t("security_to_ai_security_engineer","Security Engineer","安全工程师","AI Security Engineer","AI 安全工程师",["threat modeling","least privilege"],["威胁建模","最小权限"],["prompt injection defense","model supply-chain review"],["提示注入防御","模型供应链评审"],["led an application security review","主导过应用安全评审"]),
  t("teacher_to_learning_experience_designer","Teacher","教师","Learning Experience Designer","学习体验设计师",["learning objective design","formative feedback"],["学习目标设计","形成性反馈"],["digital interaction prototyping","learning analytics"],["数字交互原型","学习分析"],["designed a project-based unit","设计过项目式学习单元"]),
  t("finance_analyst_to_fintech_product_analyst","Financial Analyst","金融分析师","Fintech Product Analyst","金融科技产品分析师",["financial modeling","risk interpretation"],["财务建模","风险解读"],["product telemetry","regulated data design"],["产品遥测","受监管数据设计"],["built a scenario-based forecast","构建过情景预测模型"]),
  t("game_dev_to_simulation_engineer","Game Developer","游戏开发工程师","Simulation Engineer","仿真工程师",["real-time systems","physics implementation"],["实时系统","物理实现"],["numerical validation","scientific reproducibility"],["数值验证","科学可复现性"],["optimized a deterministic game loop","优化过确定性游戏循环"]),
  t("embedded_to_edge_ai_engineer","Embedded Engineer","嵌入式工程师","Edge AI Engineer","边缘 AI 工程师",["hardware constraints","low-level optimization"],["硬件约束","底层优化"],["model quantization","on-device evaluation"],["模型量化","端侧评测"],["shipped firmware under a strict power budget","在严格功耗预算下交付过固件"]),
  t("bi_analyst_to_data_product_manager","Business Intelligence Analyst","商业智能分析师","Data Product Manager","数据产品经理",["stakeholder metrics","data storytelling"],["干系人指标","数据叙事"],["data product strategy","platform governance"],["数据产品策略","平台治理"],["standardized a company KPI definition","统一过公司 KPI 定义"]),
  t("consultant_to_solution_architect","Technology Consultant","技术顾问","Solution Architect","解决方案架构师",["requirements synthesis","executive communication"],["需求归纳","高层沟通"],["architecture validation","operational ownership"],["架构验证","运营责任"],["delivered a multi-team transformation plan","交付过多团队转型方案"]),
  t("marketer_to_growth_engineer","Growth Marketer","增长营销人员","Growth Engineer","增长工程师",["funnel analysis","experiment framing"],["漏斗分析","实验设计"],["production coding","privacy-safe instrumentation"],["生产编码","隐私安全埋点"],["ran a statistically reviewed acquisition test","执行过统计评审的获客实验"]),
  t("ux_researcher_to_product_researcher","UX Researcher","用户体验研究员","Product Researcher","产品研究员",["qualitative interviewing","behavior synthesis"],["定性访谈","行为归纳"],["market triangulation","decision impact measurement"],["市场三角验证","决策影响度量"],["completed a mixed-method usability study","完成过混合方法可用性研究"]),
  t("sysadmin_to_cloud_engineer","Systems Administrator","系统管理员","Cloud Engineer","云工程师",["operational troubleshooting","access administration"],["运维排障","访问管理"],["infrastructure as code","cloud cost governance"],["基础设施即代码","云成本治理"],["managed a resilient backup rotation","管理过可靠备份轮换"]),
  t("dba_to_data_platform_engineer","Database Administrator","数据库管理员","Data Platform Engineer","数据平台工程师",["query performance","data durability"],["查询性能","数据持久性"],["stream processing","self-service platform APIs"],["流处理","自助平台 API"],["executed a zero-downtime database migration","执行过零停机数据库迁移"]),
  t("technical_writer_to_developer_advocate","Technical Writer","技术写作者","Developer Advocate","开发者关系工程师",["developer empathy","technical explanation"],["开发者同理心","技术解释"],["live demonstration","community feedback loops"],["现场演示","社区反馈闭环"],["authored an API tutorial used by customers","编写过客户使用的 API 教程"]),
  t("sales_engineer_to_ai_solution_consultant","Sales Engineer","售前工程师","AI Solution Consultant","AI 解决方案顾问",["technical discovery","solution demonstration"],["技术需求发现","方案演示"],["AI feasibility evaluation","responsible deployment design"],["AI 可行性评估","负责任部署设计"],["built a proof-of-value integration","构建过价值验证集成"]),
  t("mechanical_engineer_to_robotics_software_engineer","Mechanical Engineer","机械工程师","Robotics Software Engineer","机器人软件工程师",["system dynamics","physical prototyping"],["系统动力学","物理原型"],["robot middleware","sensor fusion"],["机器人中间件","传感器融合"],["validated a mechanism against measured loads","用实测载荷验证过机械结构"]),
  t("scientist_to_ml_research_engineer","Scientist","科学家","ML Research Engineer","机器学习研究工程师",["hypothesis testing","quantitative analysis"],["假设检验","定量分析"],["training systems","reproducible model packaging"],["训练系统","可复现模型封装"],["maintained a reproducible experiment notebook","维护过可复现实验记录"]),
  t("founder_to_ai_product_builder","Startup Founder","创业者","AI Product Builder","AI 产品构建者",["customer discovery","resource prioritization"],["客户发现","资源优先级"],["reliable AI implementation","evaluation operations"],["可靠 AI 实现","评测运营"],["validated a paid workflow problem with users","向用户验证过付费工作流问题"]),
  t("generalist_to_automation_engineer","Business Generalist","业务通才","Automation Engineer","自动化工程师",["cross-functional process mapping","manual exception handling"],["跨职能流程梳理","人工异常处理"],["software testing","safe tool integration"],["软件测试","安全工具集成"],["reduced a recurring manual reporting process","减少过重复人工报表流程"])
];

export const scenarioCatalog=[
  {id:"confirmed_quick_start",weeklyMinutes:300,claimState:"user_confirmed",evidenceState:"strong",message:{en:"I know my target and want the first practical step.","zh-CN":"我清楚目标，希望直接开始第一项实践。"},behavior:"use_confirmed_transfer"},
  {id:"natural_language_ambiguous",weeklyMinutes:180,claimState:"inferred",evidenceState:"partial",message:{en:"I want to work with AI, but I am unsure which role fits.","zh-CN":"我想从事 AI 相关工作，但不确定适合哪个岗位。"},behavior:"clarify_target_without_confirming_claim"},
  {id:"unconfirmed_inference",weeklyMinutes:240,claimState:"inferred",evidenceState:"strong",message:{en:"The imported role label may be wrong; show me why you inferred it.","zh-CN":"导入的职业标签可能不准，请说明推断依据。"},behavior:"keep_claim_inferred"},
  {id:"contradictory_evidence",weeklyMinutes:240,claimState:"disputed",evidenceState:"conflicting",message:{en:"That transfer does not match what I actually did; correct it.","zh-CN":"这条迁移关系不符合我的真实经历，请纠正。"},behavior:"create_claim_and_route_revision"},
  {id:"limited_time",weeklyMinutes:60,claimState:"user_confirmed",evidenceState:"strong",message:{en:"I only have one hour this week and need a bounded task.","zh-CN":"我这周只有一小时，需要一个范围明确的任务。"},behavior:"fit_time_budget"},
  {id:"advanced_experience",weeklyMinutes:480,claimState:"demonstrated",evidenceState:"strong",message:{en:"Do not repeat basics I already demonstrated; challenge the actual gap.","zh-CN":"不要重复我已经证明的基础内容，请针对真实缺口提高难度。"},behavior:"avoid_reteaching_demonstrated_skill"},
  {id:"difficulty_reduction",weeklyMinutes:150,claimState:"user_confirmed",evidenceState:"strong",message:{en:"The current task is too large. Keep the goal but make it easier.","zh-CN":"当前任务太大，请保留目标但降低难度。"},behavior:"reduce_scope_preserve_causality"},
  {id:"multiple_missions",weeklyMinutes:300,claimState:"user_confirmed",evidenceState:"strong",message:{en:"I have two active goals; use only the one I explicitly focused.","zh-CN":"我有两个活跃目标，只使用我明确聚焦的那个。"},behavior:"read_focus_pointer_only"},
  {id:"private_enterprise_context",weeklyMinutes:240,claimState:"user_confirmed",evidenceState:"strong",message:{en:"My company reviewer can see only the artifact revision I shared.","zh-CN":"企业评审者只能查看我明确分享的制品版本。"},behavior:"respect_share_grant"},
  {id:"create_project_completion",weeklyMinutes:420,claimState:"applied",evidenceState:"strong",message:{en:"Help me finish a bounded project with two milestones and portfolio evidence.","zh-CN":"帮助我完成一个包含两个里程碑和作品证据的有界项目。"},behavior:"require_complete_create_manifest"}
];

const localized=(value,locale)=>value[locale];
export const buildProductEval=(transition,scenario,locale,index)=>{
  const evidenceId=`evidence-${transition.id}-${scenario.id}`;
  const claimId=`claim-${transition.id}-${scenario.id}`;
  const missionId=`mission-${transition.id}`;
  const transfer=localized(transition.transfer,locale);
  const gaps=localized(transition.gaps,locale);
  return {
    id:`E2E-${locale.toUpperCase()}-${transition.id.toUpperCase()}-${String(index).padStart(2,"0")}`,
    semanticKey:`${transition.id}:${scenario.id}`,
    locale,transition:transition.id,scenario:scenario.id,
    input:{
      mission:{id:missionId,sourceRole:localized(transition.source,locale),targetRole:localized(transition.target,locale),focusVersion:scenario.id==="multiple_missions"?7:1},
      userMessage:localized(scenario.message,locale),weeklyMinutes:scenario.weeklyMinutes,
      claims:[{id:claimId,capability:transfer[0],status:scenario.claimState==="disputed"?"disputed":"active",verificationLevel:scenario.claimState==="disputed"?"user_confirmed":scenario.claimState,revision:scenario.claimState==="disputed"?2:1,evidenceIds:[evidenceId]}],
      evidence:[{id:evidenceId,status:scenario.evidenceState==="conflicting"?"disputed":"recorded",statement:localized(transition.evidence,locale),contentHash:`sha256:${transition.id}:${scenario.id}`}],
      targetRequirements:[...transfer,...gaps],currentRouteVersion:scenario.claimState==="disputed"?3:1,claimSetHash:`claims:${transition.id}:${scenario.id}`,
      privacy:{tenantId:"tenant-eval-a",shareGrant:scenario.id==="private_enterprise_context"?{resourceRevision:"artifact-r2",scope:["read"]}:null}
    },
    expected:{
      behavior:scenario.behavior,mustReferenceEvidenceIds:[evidenceId],mustReferenceClaimIds:[claimId],
      transferBridge:{from:transfer[0],to:gaps[0],requiresExplanation:true},gapsToName:gaps,
      firstTask:{targetsGap:gaps[0],maximumMinutes:Math.min(90,scenario.weeklyMinutes),mustBeBounded:true},
      claimRules:{neverUpgradeWithoutEvidence:true,keepInferred:scenario.claimState==="inferred",createNewRevision:scenario.claimState==="disputed"},
      routeRules:{expectedClaimSetHash:`claims:${transition.id}:${scenario.id}`,staleOldResult:scenario.claimState==="disputed"},
      privacyRules:{allowedResourceRevision:scenario.id==="private_enterprise_context"?"artifact-r2":null,crossTenantReads:0},
      forbidden:["exact_capability_percentage","unsupported_confirmed_claim","silent_external_write","unfocused_mission"]
    },
    rubricVersion:"growth-e2e-v1"
  };
};

export const buildProfileEval=(profile,transition,scenario,locale,index)=>{
  const base=buildProductEval(transition,scenario,locale,(index%10)+1);
  const common={mission:base.input.mission,claims:base.input.claims,evidence:base.input.evidence,locale,semanticKey:`${profile.id}:${base.semanticKey}:${index}`};
  const profileInputs={
    route_planner:{...common,targetRequirements:base.input.targetRequirements,claimSetHash:base.input.claimSetHash,baseRouteVersion:base.input.currentRouteVersion,ontologySnapshotId:"ontology-v1",contentSnapshotId:"content-v1"},
    daily_planner:{...common,acceptedRoute:{revisionId:`route-${transition.id}-accepted`,claimSetHash:base.input.claimSetHash,gaps:base.expected.gapsToName},focus:{missionId:base.input.mission.id,focusVersion:base.input.mission.focusVersion},weeklyMinutes:scenario.weeklyMinutes,recentEvidenceIds:base.expected.mustReferenceEvidenceIds},
    coach:{...common,selectedText:localized(scenario.message,locale),conversationThroughSeq:index,preference:{explanationDepth:index%2?"concise":"step_by_step"},allowedContextIds:[...base.expected.mustReferenceEvidenceIds,...base.expected.mustReferenceClaimIds],requestedAction:scenario.behavior},
    evaluator:{...common,submission:{kind:["code","writing","design"][index%3],revision:1,content:`Submission for ${transition.id} scenario ${scenario.id}`,contentHash:`submission:${transition.id}:${scenario.id}:${index}`},deterministicTests:{passed:index%5!==0,total:4,passedCount:index%5!==0?4:3},rubric:{version:"rubric-v1",dimensions:["correctness","reasoning","application"]}},
    artifact_builder:{...common,project:{id:`project-${transition.id}`,status:"active",requiredMilestones:["m1","m2"],completedMilestones:index%4===0?["m1"]:["m1","m2"],reflectionRecorded:index%4!==0},workspaceRevision:`workspace-${index}`,artifactRevisions:[`artifact-${index}-r1`],evidenceManifest:base.expected.mustReferenceEvidenceIds,externalDestination:index%7===0?"github":null,approval:index%7===0?null:{scopeHash:`scope-${index}`}}
  };
  const profileExpected={
    route_planner:{status:scenario.claimState==="disputed"?"proposed":"proposed",claimSetHash:base.input.claimSetHash,citations:base.expected.mustReferenceEvidenceIds,neverConfirmInferred:true,staleIfHashChanges:true},
    daily_planner:{missionId:base.input.mission.id,focusVersion:base.input.mission.focusVersion,causalReferences:[...base.expected.mustReferenceEvidenceIds,`route-${transition.id}-accepted`],maximumMinutes:Math.min(90,scenario.weeklyMinutes),oneTask:true},
    coach:{mustUseOnlyContextIds:[...base.expected.mustReferenceEvidenceIds,...base.expected.mustReferenceClaimIds],mustNotInventContext:true,mustNotExecuteRestrictedAction:true,teachingMove:scenario.id==="difficulty_reduction"?"smaller_step":"guided_question"},
    evaluator:{deterministicDecision:index%5===0?"rework":"pass",mayUpgradeCapability:false,evidenceRequired:true,expertAgreementTarget:0.85,modelSelfAssessmentInsufficient:true},
    artifact_builder:{completionAllowed:index%4!==0,manifestRequired:true,externalWriteAllowed:index%7!==0,dangerousContentMustBeQuarantined:true,exactWorkspaceRevision:`workspace-${index}`}
  };
  return {id:`${profile.id.toUpperCase()}-${locale.toUpperCase()}-${String(index).padStart(3,"0")}`,profile:profile.id,locale,input:profileInputs[profile.id],expected:{...profileExpected[profile.id],rubric:profile.rubric,minimumScorePerDimension:4,unauthorizedEffects:0},rubricVersion:`${profile.id}-v1`};
};
