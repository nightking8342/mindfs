/**
 * 弹簧物理 —— 标签指示器的公共底座。
 *
 * 复刻琉璃 UI（bilibili liuli-ui）的参数：位置欠阻尼会回弹一下，
 * 拉伸/挤压跟速度联动，这三条合起来才有「液体感」。
 *
 * 抽成模块是因为有两个消费者：fork 自己的 ForkGlassTabs（React 组件，
 * 用在侧栏），以及 forkTabIndicator（命令式 DOM 增强，用在上游写死的
 * 标签栏上）。两边必须同参数，否则同一个应用里两种标签栏手感不一致。
 */

export type SpringState = { value: number; velocity: number };

/** 欠阻尼弹簧的解析解，与 liuli-ui 的 springStepUnderdamped 同式 */
export function springStep(
  s: SpringState,
  target: number,
  dt: number,
  omegaN: number,
  zeta: number,
): SpringState {
  const x0 = s.value - target;
  const v0 = s.velocity;
  const omegaD = omegaN * Math.sqrt(1 - zeta * zeta);
  const decay = Math.exp(-zeta * omegaN * dt);
  const cosWd = Math.cos(omegaD * dt);
  const sinWd = Math.sin(omegaD * dt);
  const b0 = (v0 + zeta * omegaN * x0) / omegaD;
  const offset = decay * (x0 * cosWd + b0 * sinWd);
  const velocity = -zeta * omegaN * offset + decay * omegaD * (-x0 * sinWd + b0 * cosWd);
  return { value: target + offset, velocity };
}

export const POS_OMEGA = Math.sqrt(300);
export const POS_ZETA = 0.5;
export const SCALE_X_OMEGA = Math.sqrt(250);
export const SCALE_X_ZETA = 0.6;
export const SCALE_Y_OMEGA = Math.sqrt(250);
export const SCALE_Y_ZETA = 0.7;
export const THRESHOLD = 0.01;

/** 形变与速度联动：滑得越快，横向拉得越长、纵向压得越扁 */
export function deformTargets(speed: number): { scaleX: number; scaleY: number } {
  return {
    scaleX: 1 + Math.min(speed / 900, 0.28),
    scaleY: 1 - Math.min(speed / 1800, 0.14),
  };
}
