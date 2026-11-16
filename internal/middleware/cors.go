package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

const (
	corsAllowMethods = "GET, POST, PUT, DELETE, OPTIONS"
	// 前端用 Bearer token，所以 Authorization 必须在允许名单里；
	// 以后前端新增自定义请求头，这里要同步加，否则浏览器会拦掉整个请求。
	corsAllowHeaders = "Authorization, Content-Type, X-Request-ID"
	// 默认情况下前端 JS 只能读到少数几个响应头。这两个要显式暴露：
	// X-Request-ID 用于用户报障时给追踪号，Retry-After 用于 429 之后知道等多久。
	corsExposeHeaders = "X-Request-ID, Retry-After"
	corsMaxAge        = "600"
)

// CORS 按白名单给跨域响应补上 Access-Control-Allow-* 头。
//
// 背景：浏览器有同源策略，页面在 A 域名、接口在 B 域名时算跨域。
// 跨域请求即使服务端正常返回 200，只要响应里没有 Allow-Origin 头，
// 浏览器也不会把结果交给 JS，前端看到的是 CORS 报错而不是数据。
//
// 本项目只放行配置里显式列出的 Origin。因为用 Bearer token 而不是 Cookie，
// 不需要发 Allow-Credentials，但也不用通配符蒙过去：回显具体 Origin 更可控。
func CORS(allowedOrigins []string) (gin.HandlerFunc, error) {
	// 启动期把配置整理成查表用的 map，填错的值直接让服务起不来。
	origins := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin == "*" {
			// 通配符等于对全网开放，且无法与凭据请求共存，只接受精确 Origin。
			return nil, errors.New("middleware: CORS wildcard origin is not supported")
		}
		// 合法值长这样：https://example.com 或 http://127.0.0.1:5173。
		// 带路径或查询串说明填错了（那是 URL 不是 Origin），启动期就拒绝。
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			return nil, fmt.Errorf("middleware: invalid CORS origin %q", origin)
		}
		origins[origin] = true
	}

	return func(c *gin.Context) {
		// 同一个 URL 对不同 Origin 会返回不同的允许头，缓存层必须按 Origin 分开存，
		// 否则 A 域名的响应被缓存后会错发给 B 域名。
		// 这一行对所有请求都要加，包括不在白名单的。
		c.Writer.Header().Add("Vary", "Origin")

		origin := c.GetHeader("Origin")
		if !origins[origin] {
			// 不在白名单（或压根不是跨域请求）：不带任何 Allow-* 头。
			//
			// 预检请求直接 204 结束，浏览器看到没有 Allow-Origin 就判定失败，
			// 不会再把真正的请求发出去。
			//
			// 普通请求仍然放行给业务处理，只是响应缺 CORS 头，由浏览器拦读取。
			// 这样做的好处是服务端不把「跨域被拒」伪装成业务错误：
			// 状态码、日志都和真实情况一致，排查时不会被误导。
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
			return
		}

		// 回显具体 Origin 而不是写 *：配合上面的 Vary，多域名白名单才能被缓存正确处理。
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Expose-Headers", corsExposeHeaders)

		// 预检（OPTIONS）到此结束，不进业务 handler。
		// 浏览器在发带 Authorization 或 JSON 体的请求前，会先自动发这一次 OPTIONS 问许可，
		// 这里把「允许哪些方法、哪些头、能缓存多久」一次性答完。
		if c.Request.Method == http.MethodOptions { // 如果是预检请求 则走这个
			c.Header("Access-Control-Allow-Methods", corsAllowMethods)
			c.Header("Access-Control-Allow-Headers", corsAllowHeaders)
			c.Header("Access-Control-Max-Age", corsMaxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}, nil
}
