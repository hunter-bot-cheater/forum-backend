import request from '@/utils/request'

// 本文件是后端 internal/upload 模块的镜像。
//
// 上传只做一件事：把本地图片交给服务端，拿回一个可直接访问的地址。
// 压缩、缩放、摆正全在服务端完成（见 internal/upload），
// 前端不做任何图像处理：浏览器里压缩要把整张图读进内存再重编码，
// 低端手机上传几张就可能被系统杀掉页面。

// UploadedImage 是一次上传的结果。
export interface UploadedImage {
  // url 是站内相对地址（/uploads/avatars/2026/11/xxx.jpg），
  // 直接写进 PUT /users/me 的 avatar_url 即可。同域部署时前端不需要拼域名。
  url: string
  width: number
  height: number
  // bytes 是压缩后的大小，original_bytes 是原图大小。
  // 两个都返回是为了能显示「267 KB → 24 KB」：用户看到这个差值，
  // 才知道服务端确实压过了，不会怀疑图被弄坏。
  bytes: number
  original_bytes: number
  format: 'jpeg' | 'png'
}

// uploadAvatar 上传一张头像图片。
//
// 不要手写 Content-Type：multipart 的 boundary 是浏览器现生成的，
// 手动写死 "multipart/form-data" 会丢掉 boundary，服务端解析不出字段。
// 超时给 30 秒而不是默认的 10 秒：手机网络下 5 MB 的图传不完。
export function uploadAvatar(file: File) {
  const form = new FormData()
  form.append('file', file)
  return request<UploadedImage>({
    url: '/uploads/avatar',
    method: 'post',
    data: form,
    timeout: 30000,
  })
}
