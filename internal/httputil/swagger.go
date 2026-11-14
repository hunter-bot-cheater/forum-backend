package httputil

// SwaggerEnvelope 描述统一响应壳，只用于生成 API 文档。
// Data 是 interface{}，具体形状由每个接口的 @Success 注解给出。
// 这个结构体不参与运行时代码，不要传给 response.OK。
//
// ErrorCode 与 RequestID 只在失败响应里出现，成功响应不带这两个字段。
// 放在同一个壳里是为了让文档能查到机读错误码，前端按 error_code 分支，
// 不用依赖中文文案。
type SwaggerEnvelope struct {
	Code      int         `json:"code"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data"`
	ErrorCode string      `json:"error_code,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
}
