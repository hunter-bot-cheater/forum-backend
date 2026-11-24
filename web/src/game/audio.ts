// 台球音效：全部用 Web Audio 现场合成，一个音频文件都不带。
//
// 为什么不外挂 mp3：
//   1. 撞击音要跟着力度变——力度越大越响、越脆。用采样就得为每一档录一份，
//      包里凭空多出几百 KB，而合成只是改两个参数；
//   2. 开球那一杆几十颗球几乎同时响，采样没法做「同一份素材叠三层还不糊」，
//      合成可以用压限器统一收；
//   3. 采集真实球房的录音有版权问题。
// 代价是「像不像真的」差一点，靠包络（3ms 起音、几十毫秒收尾）与滤波尽量
// 贴住「皮革撞硬球」那种短促、偏高、没有余音的手感。
//
// 浏览器不允许没有用户手势就出声，所以整套东西走 unlock()：真正创建
// AudioContext 的时机是页面第一次 pointerdown，在那之前所有调用都是空转。
import { sampleAt, type ShotFrame } from './animator'
import type { ShotEvent } from './protocol'
import { TABLE_LENGTH } from './table'

// SfxKind 是四类撞击音。结算、自由球那几个提示音不在这套事件流里，
// 由页面直接调 win()/lose()/freeBall()。
export type SfxKind = 'strike' | 'click' | 'cushion' | 'pocket'

// SfxCue 是「第几秒该响什么」。
//
// 它由一杆的事件流在开播前一次性算好（见 buildShotSfx），播放时按时间游标
// 往外放——这样不在每帧里扫一遍事件，也不会因为掉帧漏掉声音。
export interface SfxCue {
  t: number
  kind: SfxKind
  // 0~1，按撞击前的球速折算。越重越响，也越亮。
  gain: number
  // -1~1 左右声道：球在左半台就从左边响，右半台从右边响。
  pan: number
}

// 关掉音效记在这里，刷新页面仍然安静。
const STORAGE_KEY = 'forum_sound'
// 总音量。单个音的增益都按这个量级标定过，改它等于整体推子。
const MASTER_GAIN = 0.5

// readEnabled 读设置。没存过就是开——第一次进对局有声音才叫「默认背景音效」，
// 想安静的人按一下按钮即可，选择会记住。
function readEnabled(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) !== 'off'
  } catch {
    return true
  }
}

// makeNoise 造一段 1 秒的白噪声，所有「撞击」都从它滤波得来。
//
// 复用同一份 buffer 而不是每次新建：BufferSource 只能播一次，但 buffer 可以
// 被任意多个 source 共享，几十次撞击也只占一份内存。
function makeNoise(ctx: AudioContext): AudioBuffer {
  const buffer = ctx.createBuffer(1, ctx.sampleRate, ctx.sampleRate)
  const data = buffer.getChannelData(0)
  for (let i = 0; i < data.length; i += 1) {
    data[i] = Math.random() * 2 - 1
  }
  return buffer
}

// Sfx 是一套最小的合成器。做成类而不是一堆裸函数，是为了把 ctx、噪声 buffer、
// 环境音这几样状态收在一处——它们必须共享同一个 AudioContext。
class Sfx {
  private ctx: AudioContext | null = null
  private master: GainNode | null = null
  private noise: AudioBuffer | null = null
  private ambienceStarted = false
  private enabledFlag = readEnabled()

  get enabled(): boolean {
    return this.enabledFlag
  }

  // setEnabled 开关总音量并记住选择。
  //
  // 关掉要顺手停掉环境音：只把 master 拉到 0 的话，那几个振荡器还在跑，
  // 手机上一次对局要耗掉百分之几的电。
  setEnabled(on: boolean) {
    this.enabledFlag = on
    try {
      localStorage.setItem(STORAGE_KEY, on ? 'on' : 'off')
    } catch {
      // 隐私模式下写不进去，只是记不住选择，不影响本次对局。
    }
    if (on) {
      this.unlock()
      return
    }
    this.stopAmbience()
  }

  // suspend 停掉环境音并释放 context。
  //
  // 离开对局页必须调它：那层垫音是这一页的氛围，回到大厅还在响就成了
  // 「关不掉的背景音乐」。释放之后下次 unlock 会重新建一套，不用额外收拾。
  suspend() {
    this.stopAmbience()
    // 没有起过环境音（一直静音、或还没解锁）时 context 可能还在：
    // 一并关掉，别让手机上留一个常驻的音频线程。
    if (!this.ctx) {
      return
    }
    const ctx = this.ctx
    this.ctx = null
    this.master = null
    this.noise = null
    void ctx.close()
  }

  // unlock 在第一次用户手势时调用：建 context、恢复它、起环境音。
  //
  // 重复调用是安全的（手势监听会一直挂着）：ctx 已存在、环境音已在播都直接返回。
  unlock() {
    const ctx = this.ensure()
    if (!ctx) {
      return
    }
    if (ctx.state === 'suspended') {
      void ctx.resume()
    }
    if (this.enabledFlag) {
      this.startAmbience()
    }
  }

  // play 放一条撞击音。kind 之外的东西（力度、方位）都已经在 cue 里。
  play(cue: SfxCue) {
    if (!this.enabledFlag) {
      return
    }
    const ctx = this.ensure()
    if (!ctx || !this.master) {
      return
    }
    if (ctx.state === 'suspended') {
      void ctx.resume()
    }
    // 事件时间戳是「这一杆开始后第几秒」，播放时刻是「现在」：
    // 帧粒度 120Hz，最多晚 8 毫秒，人耳分不出来。
    const when = ctx.currentTime + 0.001
    if (cue.kind === 'strike') {
      this.strike(when, cue.gain, cue.pan)
      return
    }
    if (cue.kind === 'click') {
      this.click(when, cue.gain, cue.pan)
      return
    }
    if (cue.kind === 'cushion') {
      this.cushion(when, cue.gain, cue.pan)
      return
    }
    this.pocket(when, cue.pan)
  }

  // strike 是杆头击打母球：一声闷响加一点皮头摩擦的脆音。
  strike(when: number, gain: number, pan: number) {
    if (!this.enabledFlag) {
      return
    }
    this.burst(when, {
      dur: 0.03,
      freq: 1500,
      q: 0.9,
      gain: 0.16 + 0.16 * gain,
      filter: 'bandpass',
      pan,
    })
    this.tone(when, { freq: 96, dur: 0.09, gain: 0.12 + 0.12 * gain, pan })
  }

  // click 是球撞球：清脆的「嗒」。
  //
  // 噪声过带通给瞬态，再叠一颗 1.1kHz 的正弦给「实体」——纯噪声听起来像
  // 纸片摩擦，加了这一层才像硬球。力度越大带通越靠上，声音跟着变脆。
  click(when: number, gain: number, pan: number) {
    if (!this.enabledFlag) {
      return
    }
    this.burst(when, {
      dur: 0.026,
      freq: 1900 + 1300 * gain,
      q: 1.4,
      gain: 0.2 + 0.22 * gain,
      filter: 'bandpass',
      pan,
    })
    this.tone(when, { freq: 1080 + 240 * gain, dur: 0.032, gain: 0.1 + 0.14 * gain, pan })
  }

  // cushion 是撞库：比撞球闷，能量被橡皮吃掉大半。
  cushion(when: number, gain: number, pan: number) {
    if (!this.enabledFlag) {
      return
    }
    this.burst(when, {
      dur: 0.055,
      freq: 480,
      q: 0.8,
      gain: 0.16 + 0.2 * gain,
      filter: 'lowpass',
      pan,
    })
    this.tone(when, { freq: 104, dur: 0.07, gain: 0.08 + 0.1 * gain, pan })
  }

  // pocket 是落袋：一声下滑的「咚」，后面跟两下轻磕（球在袋筒里碰壁）。
  pocket(when: number, pan: number) {
    if (!this.enabledFlag) {
      return
    }
    this.tone(when, { freq: 250, freqTo: 68, dur: 0.17, gain: 0.3, pan })
    this.burst(when, {
      dur: 0.05,
      freq: 1100,
      q: 0.7,
      gain: 0.12,
      filter: 'lowpass',
      pan,
    })
    this.burst(when + 0.07, {
      dur: 0.03,
      freq: 900,
      q: 0.7,
      gain: 0.07,
      filter: 'lowpass',
      pan,
    })
    this.burst(when + 0.115, {
      dur: 0.026,
      freq: 780,
      q: 0.7,
      gain: 0.05,
      filter: 'lowpass',
      pan,
    })
  }

  // placeCue 是把手里的白球放到台面上：一声很轻的「嗒」，不要盖过对局。
  placeCue() {
    if (!this.enabledFlag) {
      return
    }
    const ctx = this.ensure()
    if (!ctx) {
      return
    }
    const when = ctx.currentTime + 0.001
    this.burst(when, {
      dur: 0.035,
      freq: 1200,
      q: 0.9,
      gain: 0.14,
      filter: 'lowpass',
      pan: 0,
    })
    this.tone(when, { freq: 190, dur: 0.05, gain: 0.1, pan: 0 })
  }

  // freeBall 是「拿到自由球」：两声上行的小提示，提示的是「现在该你摆球了」。
  freeBall() {
    this.notes([659.25, 880], 0.1, 0.14)
  }

  // chatIn 是收到对面发来的消息：两声上行的「叮咚」。
  //
  // 音量比 freeBall 低一档、音色用三角波：提示要听得见，但不能在玩家正瞄准时
  // 把人吓一跳。间隔也压得短，两句连着来才像聊天，不像连报两次警报。
  chatIn() {
    this.notes([880, 1174.66], 0.075, 0.1)
  }

  // chatOut 是自己把消息发出去：一声很轻的高音，只给自己一个「发出去了」的确认。
  chatOut() {
    this.notes([1318.51], 0.05, 0.06, 'sine')
  }

  // win 是这一局赢下来：一个上行大三和弦琶音，最后落在高八度。
  win() {
    this.notes([523.25, 659.25, 783.99, 1046.5], 0.11, 0.16)
  }

  // lose 是输了：三个下行音，音色比 win 暗一档（正弦 vs 三角）。
  lose() {
    this.notes([392, 329.63, 261.63], 0.15, 0.14, 'sine')
  }

  // notes 把一串音按固定间隔排成琶音。
  private notes(freqs: number[], gap: number, gain: number, type: OscillatorType = 'triangle') {
    if (!this.enabledFlag) {
      return
    }
    const ctx = this.ensure()
    if (!ctx) {
      return
    }
    const base = ctx.currentTime + 0.02
    for (let i = 0; i < freqs.length; i += 1) {
      this.tone(base + i * gap, { freq: freqs[i], dur: gap * 2.2, gain, pan: 0, type })
    }
  }

  // ensure 建 context 与主链路。没有 AudioContext（jsdom、老浏览器）时返回 null，
  // 上层一律当作「没声音」，不抛错——音效不该让对局挂掉。
  private ensure(): AudioContext | null {
    if (this.ctx) {
      return this.ctx
    }
    const Ctor =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
    if (!Ctor) {
      return null
    }
    const ctx = new Ctor()
    const master = ctx.createGain()
    master.gain.value = MASTER_GAIN
    // 开球那一杆几十颗球几乎同一瞬间撞上，几十份增益直接叠加会削顶，
    // 听起来是「啪」一下爆音。压限器只在那一下压低，安静时一点不损失音量。
    const comp = ctx.createDynamicsCompressor()
    comp.threshold.value = -14
    comp.ratio.value = 6
    comp.attack.value = 0.003
    comp.release.value = 0.12
    master.connect(comp)
    comp.connect(ctx.destination)
    this.ctx = ctx
    this.master = master
    this.noise = makeNoise(ctx)
    return ctx
  }

  // startAmbience 起环境音：一层很低的持续垫音加一点房间底噪。
  //
  // 音量刻意压到「关掉时才注意到刚才有声音」的程度：这是对局，不是听歌。
  private startAmbience() {
    if (this.ambienceStarted) {
      return
    }
    const ctx = this.ensure()
    if (!ctx || !this.master || !this.noise) {
      return
    }
    // 还没解锁（表盘上没有过手势）就先不起：此刻 start 不会出声，
    // 反而会把 ambienceStarted 置真，之后永远起不来。
    if (ctx.state === 'suspended') {
      return
    }
    this.ambienceStarted = true
    const now = ctx.currentTime
    const bus = ctx.createGain()
    bus.gain.setValueAtTime(0.0001, now)
    bus.gain.exponentialRampToValueAtTime(0.05, now + 2)
    bus.connect(this.master)

    // 垫音：A2 与 E3 两个正弦，慢速颤音让它不呆板。
    const freqs = [110, 164.81]
    const lfo = ctx.createOscillator()
    lfo.frequency.value = 0.07
    const lfoGain = ctx.createGain()
    lfoGain.gain.value = 0.012
    lfo.connect(lfoGain)
    lfoGain.connect(bus.gain)
    lfo.start(now)
    for (const freq of freqs) {
      const osc = ctx.createOscillator()
      osc.type = 'sine'
      osc.frequency.value = freq
      const g = ctx.createGain()
      g.gain.value = 0.5
      const filter = ctx.createBiquadFilter()
      filter.type = 'lowpass'
      filter.frequency.value = 420
      osc.connect(g)
      g.connect(filter)
      filter.connect(bus)
      osc.start(now)
    }

    // 底噪：极低的低通噪声，把「垫音」和「完全静音」之间那道台阶填掉。
    const src = ctx.createBufferSource()
    src.buffer = this.noise
    src.loop = true
    const noiseFilter = ctx.createBiquadFilter()
    noiseFilter.type = 'lowpass'
    noiseFilter.frequency.value = 260
    const noiseGain = ctx.createGain()
    noiseGain.gain.value = 0.22
    src.connect(noiseFilter)
    noiseFilter.connect(noiseGain)
    noiseGain.connect(bus)
    src.start(now)
  }

  // stopAmbience 停掉环境音。振荡器不 stop 会一直占着音频线程。
  private stopAmbience() {
    if (!this.ambienceStarted || !this.ctx || !this.master) {
      return
    }
    this.ambienceStarted = false
    // 直接断开 master 上的总线：环境音的节点都挂在它下面，断开即静音，
    // 再把 context 挂起，省掉手机上的持续唤醒。
    this.master.disconnect()
    const ctx = this.ctx
    this.ctx = null
    this.master = null
    this.noise = null
    void ctx.close()
  }

  // burst 是一段带包络的滤波噪声，所有「撞击」的瞬态都靠它。
  private burst(
    when: number,
    opts: {
      dur: number
      freq: number
      q: number
      gain: number
      filter: BiquadFilterType
      pan: number
    },
  ) {
    const ctx = this.ctx
    const master = this.master
    if (!ctx || !master || !this.noise) {
      return
    }
    const src = ctx.createBufferSource()
    src.buffer = this.noise
    // 只有 1 秒素材，短包络内循环不到接缝处；不循环的话超过 1 秒会静音。
    src.loop = true
    const filter = ctx.createBiquadFilter()
    filter.type = opts.filter
    filter.frequency.value = opts.freq
    filter.Q.value = opts.q
    const g = ctx.createGain()
    // 起音 3 毫秒：从 0 直接跳到满值会有一声「咔」，那是包络本身的爆音。
    g.gain.setValueAtTime(0.0001, when)
    g.gain.exponentialRampToValueAtTime(Math.max(0.0002, opts.gain), when + 0.003)
    g.gain.exponentialRampToValueAtTime(0.0001, when + opts.dur)
    const panner = ctx.createStereoPanner()
    panner.pan.value = opts.pan
    src.connect(filter)
    filter.connect(g)
    g.connect(panner)
    panner.connect(master)
    src.start(when)
    src.stop(when + opts.dur + 0.02)
  }

  // tone 是一个（可选滑音的）正弦音。
  private tone(
    when: number,
    opts: {
      freq: number
      freqTo?: number
      dur: number
      gain: number
      pan: number
      type?: OscillatorType
    },
  ) {
    const ctx = this.ctx
    const master = this.master
    if (!ctx || !master) {
      return
    }
    const osc = ctx.createOscillator()
    osc.type = opts.type ?? 'sine'
    osc.frequency.setValueAtTime(opts.freq, when)
    if (opts.freqTo) {
      osc.frequency.exponentialRampToValueAtTime(opts.freqTo, when + opts.dur)
    }
    const g = ctx.createGain()
    g.gain.setValueAtTime(0.0001, when)
    g.gain.exponentialRampToValueAtTime(Math.max(0.0002, opts.gain), when + 0.004)
    g.gain.exponentialRampToValueAtTime(0.0001, when + opts.dur)
    const panner = ctx.createStereoPanner()
    panner.pan.value = opts.pan
    osc.connect(g)
    g.connect(panner)
    panner.connect(master)
    osc.start(when)
    osc.stop(when + opts.dur + 0.02)
  }
}

// 全局只有一个实例：AudioContext 是稀缺资源，浏览器给每个页面的数量有限
// （Safari 只给四个），每局新开一个会越来越哑。
export const sfx = new Sfx()

// 取某颗球在 t 时刻的速度（m/s 量级，但用「每秒多少米」的位移速率近似）。
//
// 事件本身不带撞击力度，只能从轨迹里反推：取事件前 20 毫秒的位置差。
// 20 毫秒是两帧半，够躲开关键帧坐标只保留 0.1 毫米的量化噪声。
function speedAt(frames: ReadonlyArray<ShotFrame>, id: string, t: number): number {
  const dt = 0.02
  const from = Math.max(0, t - dt)
  const before = findBall(frames, id, from)
  const at = findBall(frames, id, t)
  if (!before || !at) {
    return 0
  }
  return Math.hypot(at[0] - before[0], at[1] - before[1]) / (t - from || dt)
}

// findBall 取一帧里某颗球的位置，找不到（已经落袋）返回 null。
function findBall(frames: ReadonlyArray<ShotFrame>, id: string, t: number): [number, number] | null {
  const sample = sampleAt(frames, t)
  for (const ball of sample) {
    if (ball.id === id) {
      return [ball.x, ball.y]
    }
  }
  return null
}

// buildShotSfx 把一杆的事件流翻译成「第几秒响什么」。
//
// 力度只给击球音用（那是杆头打的），撞击音全部按撞击前的球速算——
// 一杆满力开球里既有 4 m/s 的正碰，也有撞了三次库之后 0.2 m/s 的轻碰，
// 统一用一个音量会让整杆听起来像一个人在鼓掌。
export function buildShotSfx(
  frames: ReadonlyArray<ShotFrame>,
  events: ReadonlyArray<ShotEvent>,
  power: number,
): SfxCue[] {
  const cues: SfxCue[] = []
  for (const event of events) {
    const pos = event.a ? findBall(frames, event.a, event.t) : null
    const pan = pos ? Math.max(-1, Math.min(1, (pos[0] / TABLE_LENGTH) * 2 - 1)) * 0.65 : 0
    if (event.type === 'stick_ball') {
      cues.push({ t: event.t, kind: 'strike', gain: clamp01(power), pan })
      continue
    }
    if (event.type === 'ball_pocket') {
      cues.push({ t: event.t, kind: 'pocket', gain: 1, pan })
      continue
    }
    // 撞球与撞库按相对速度取音量：撞球还要看对方那颗的来势，
    // 只看自己会把「静止的球被高速球撞」算成 0，那一下恰恰是最响的。
    let speed = event.a ? speedAt(frames, event.a, event.t) : 0
    if (event.type === 'ball_ball' && event.b) {
      speed = Math.max(speed, speedAt(frames, event.b, event.t))
    }
    if (event.type === 'ball_ball') {
      cues.push({ t: event.t, kind: 'click', gain: clamp01(speed / 3.2), pan })
      continue
    }
    if (event.type === 'cushion') {
      cues.push({ t: event.t, kind: 'cushion', gain: clamp01(speed / 3.2), pan })
    }
  }
  return cues
}

// clamp01 收进 0 到 1。速度上限按 3.2 m/s 折满：再快的碰撞听起来已经一样响了。
function clamp01(n: number): number {
  if (n < 0) {
    return 0
  }
  if (n > 1) {
    return 1
  }
  return n
}
