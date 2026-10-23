# Gate 视频素材与计量合同

公共 Core 请求仍复用 `VideoGenerationInput` / `VideoGenerationParameters`。Gate 将 `ExtraParams[gate.ExtraParamInputReferences]` 解码为 `[]GateInputReference`：image 的角色是 first_frame、last_frame、reference；video 是 reference_video；audio 是 reference_audio。地址必须为具有主机名的 HTTPS URL，禁止 userinfo。首帧、尾帧各最多一个，未知字段与类型/角色组合直接拒绝。旧 SDK 的单图 `Input.InputReference` 仍映射到 first_frame，但不能同时使用扩展素材数组。

后续新增视频 adapter 必须自行实现其协议的素材、规格和计量映射；不要依赖 Core 根据角色自动推断上游字段。

成功解析 completed 查询后，`gate.VideoUsageFromContext` 返回与标准响应相同的带 provider 后缀任务 ID、可空 `*schemas.BifrostLLMUsage`、`VideoUsageMetadata` 和存在标记。调用开始清除上一次结果，身份不匹配、协议错误和非 completed 状态不留下用量。`ok=true` 仅说明终态事实存在；Token 计费必须再检查 usage 非空。标准 `VideoUsage.Cost`、原始 settled 金额 accessor 和响应投影保持原有行为。

计量首先采用明确的 `usage.completion_tokens` 或 `usage.output_tokens`；二者同时存在必须一致。明确的零有效。负数、小数、溢出或冲突不产生 Token，也不以模型推导掩盖非法上游值。`total_tokens` 不代表输出，永不直接采用。Gate 当前公开文档没有保证提供这些扩展字段。

没有明确输出 Token 时，仅精确模型名 `bytedance/seedance-2.0` 使用终态 duration、resolution、aspect_ratio 与独立的官方规格表。`frames=duration*24+1`，`raw=width*height*frames/1024`，向上取整为 CompletionTokens。规则版本是 `seedance-2.0-24fps-plus-one-v1`。整数时长沿用 Gate wire 合同；不通过请求参数补齐终态事实，不从 billed_cost 反推。未知模型、未知规格、缺失或非法时长、溢出均保留事实但不推导。480p、720p、1080p、4k 共 24 种像素组合有固定金样；1024p/adaptive 不具有此模型规则。

来源、规则版本、真实像素、推导帧数以及取整前后 Token 留在最小 provider 元数据内。不能将真实像素替换为代表性计费尺寸，也不能把附加费塞进 usage.Cost。本次不修改 Core 分发、标准 Usage 类型、Framework 算法或其他 provider。

本地验证：Gate 全包与 race 测试仅使用 httptest；覆盖完整素材映射、非法素材零出网、false/0 保留、87300/731025/40594.5→40595、全部 24 个规格、显式 Token/缺失/非法、身份错配、context 重用及并发隔离。
