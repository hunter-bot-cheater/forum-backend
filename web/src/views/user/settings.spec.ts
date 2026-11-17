import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ElementPlus from 'element-plus'

// 编辑资料页的用例。锁两条规则：
//   1. 头像图打不开时必须说出来。el-avatar 加载失败会把 <img> 摘掉、悄悄渲染回首字，
//      外观和「没设头像」完全一样，用户只会以为「头像没变」。
//   2. 选完图就上传并落库，不让用户再点一次保存。
//
// 页面已经不接受手填地址（只保留上传），所以失败提示只剩一种来源：
// 以前留下的外链失效了，或者对方站点开了防盗链。

const { updateMeApi, uploadApi, replace, push, back } = vi.hoisted(() => ({
  updateMeApi: vi.fn(),
  uploadApi: vi.fn(),
  replace: vi.fn(),
  push: vi.fn(),
  back: vi.fn(),
}))

vi.mock('@/api/upload', () => ({
  uploadAvatar: (...args: unknown[]) => uploadApi(...args),
}))

vi.mock('@/api/user', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/user')>()
  return {
    ...actual,
    updateMe: (...args: unknown[]) => updateMeApi(...args),
  }
})

// 页面里用到 useRouter 与 onBeforeRouteLeave。
// 后者在没有路由上下文时会打一条警告，这里换成空实现；
// 但整个模块不能整体替换：@/utils/request 在 401 时要 import 真实 router 实例。
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    useRouter: () => ({ push, replace, back }),
    onBeforeRouteLeave: () => {},
  }
})

const { default: SettingsPage } = await import('@/views/user/settings.vue')
const { useUserStore } = await import('@/store/modules/user')

function profile(avatarURL: string) {
  return {
    id: 1,
    username: 'learner_001',
    nickname: '一号',
    avatar_url: avatarURL,
    bio: '',
    has_custom_nickname: true,
    created_at: '2026-11-01T10:00:00+08:00',
    following_count: 0,
    follower_count: 0,
    is_following: false,
    is_admin: false,
  }
}

// mountPage 直接把资料塞进 store，页面挂载后不会再去请求 /users/me。
function mountPage(avatarURL = '') {
  const user = useUserStore()
  user.profile = profile(avatarURL)
  user.profileLoaded = true
  return mount(SettingsPage, { global: { plugins: [ElementPlus] } })
}

// emitAvatarError 模拟浏览器加载图片失败。
// 不能靠真的加载：jsdom 不会下载图片，永远不触发 error。
async function emitAvatarError(wrapper: ReturnType<typeof mountPage>) {
  const avatar = wrapper.findComponent({ name: 'ElAvatar' })
  avatar.vm.$emit('error', new Event('error'))
  await flushPromises()
}

// chooseFile 模拟在原生 file 控件里选中一个文件。
// jsdom 里打不开文件选择框，直接把 files 换掉再触发 change。
async function chooseFile(wrapper: ReturnType<typeof mountPage>) {
  const input = wrapper.find('input[type="file"]')
  const file = new File(['x'], 'photo.jpg', { type: 'image/jpeg' })
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
  await flushPromises()
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
  uploadApi.mockReset()
  updateMeApi.mockReset()
  // 每个用例从「没有上一页」开始，需要上一页的自己写。
  window.history.replaceState({}, '')
})

describe('头像', () => {
  it('页面只留上传，不再提供手填地址的输入框', async () => {
    const wrapper = mountPage()
    await flushPromises()

    // 头像区没有可输入的控件，只有「上传头像」按钮和隐藏的 file。
    const avatarField = wrapper.findAll('.field')[0]
    expect(avatarField.find('input[type="text"]').exists()).toBe(false)
    expect(avatarField.find('input[type="file"]').exists()).toBe(true)
    expect(avatarField.find('.field__row .el-button').text()).toBe('上传头像')
  })

  it('旧外链的图打不开时明说原因，并且不挡着重新上传', async () => {
    const wrapper = mountPage('https://img95.699pic.com/photo/50464/9776.jpg_wh860.jpg')
    await flushPromises()

    // 刚进页面、图还没报错，走的是普通说明。
    expect(wrapper.find('.field__hint').text()).toContain('上传头像')

    await emitAvatarError(wrapper)

    // 提示要指到「外链失效 / 被屏蔽」，只说「加载失败」用户会一直以为是自己没保存。
    const hint = wrapper.find('.field__hint')
    expect(hint.text()).toContain('打不开')
    expect(hint.classes()).toContain('is-error')
    expect(wrapper.find('.settings__stat').text()).toBe('头像图片打不开')

    // 上传按钮必须还能点：唯一的修复路径就是重新传一张。
    const uploadButton = wrapper.find('.field__row .el-button')
    expect(uploadButton.attributes('disabled')).toBeUndefined()
  })

  it('选完图立刻上传并落库，不用再点保存', async () => {
    const wrapper = mountPage()
    await flushPromises()

    uploadApi.mockResolvedValue({
      url: '/uploads/avatars/2026/11/abc.jpg',
      width: 256,
      height: 171,
      bytes: 16470,
      original_bytes: 267313,
      format: 'jpeg',
    })
    updateMeApi.mockResolvedValue(profile('/uploads/avatars/2026/11/abc.jpg'))

    await chooseFile(wrapper)

    expect(uploadApi).toHaveBeenCalledTimes(1)
    // 上传完必须自己落库：让用户再点一次保存，他会以为上传没成功。
    expect(updateMeApi).toHaveBeenCalledWith({
      avatar_url: '/uploads/avatars/2026/11/abc.jpg',
    })
    // 预览换成服务端返回的地址：贴不上就说明「传上去了但看不到」。
    expect(wrapper.find('.field__avatar img').attributes('src')).toBe(
      '/uploads/avatars/2026/11/abc.jpg',
    )
    // 压缩结果要说出来：从 261 KB 到 16 KB，用户才知道服务端压过了，
    // 不会以为是图传坏了。
    const note = wrapper.find('.field__note').text()
    expect(note).toContain('261 KB')
    expect(note).toContain('16 KB')
  })

  it('重新上传成功后旧的失败提示要消失', async () => {
    const wrapper = mountPage('https://img95.699pic.com/photo/50464/9776.jpg_wh860.jpg')
    await flushPromises()
    await emitAvatarError(wrapper)
    expect(wrapper.find('.field__hint').text()).toContain('打不开')

    uploadApi.mockResolvedValue({
      url: '/uploads/avatars/2026/11/def.jpg',
      width: 256,
      height: 256,
      bytes: 9000,
      original_bytes: 9000,
      format: 'jpeg',
    })
    updateMeApi.mockResolvedValue(profile('/uploads/avatars/2026/11/def.jpg'))
    await chooseFile(wrapper)

    // el-avatar 只在失败时发事件，加载成功没有回调，
    // 所以必须在地址变化时主动清掉标记，否则提示会一直挂着。
    expect(wrapper.find('.field__hint').text()).not.toContain('打不开')
    expect(wrapper.find('.settings__stat').text()).not.toBe('头像图片打不开')
  })
})

describe('返回', () => {
  it('有上一页时回上一页，而不是固定推自己的主页', async () => {
    // 固定 push 自己的主页 + 主页的「返回」再 back，就是用户看到的死循环。
    window.history.replaceState({ back: '/articles' }, '')
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('.settings__back').trigger('click')

    expect(back).toHaveBeenCalledTimes(1)
    expect(push).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('直接打开这个地址（没有上一页）时退回自己的主页', async () => {
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('.settings__back').trigger('click')

    expect(back).not.toHaveBeenCalled()
    expect(replace).toHaveBeenCalledWith('/users/1')
  })
})
