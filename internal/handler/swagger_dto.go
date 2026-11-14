package handler

// SwaggerEnvelope 描述统一响应壳，只用于生成 API 文档。
// Data 是 interface{}，具体形状由每个接口的 @Success 注解给出。
// 这个结构体不参与运行时代码，不要传给 response.OK。
type SwaggerEnvelope struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}
