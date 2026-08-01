import { defineConfig, mergeConfig } from "vite";
import baseConfig from "./vite.config";

/**
 * fork 专用的 dev server 配置。
 *
 * 上游 vite.config.ts 的 proxy 硬编码指向 http://localhost:7331，而本机常驻的
 * mindfs 跑在 https://localhost:7766（config.json 里 tls: true，自签证书）。
 * 与其改上游文件，不如在这里继承并只覆盖 proxy —— 对上游零 diff。
 *
 * 用法：
 *   cd web && npx vite --config vite.fork.config.ts --host
 *   MINDFS_DEV_TARGET=https://localhost:9000 npx vite --config vite.fork.config.ts
 */
const target = process.env.MINDFS_DEV_TARGET || "https://localhost:7766";
const wsTarget = target.replace(/^http/, "ws");

export default defineConfig(
  mergeConfig(baseConfig, {
    server: {
      proxy: {
        // secure: false —— 后端是自签证书，不跳过校验代理会直接 502
        "/api": { target, changeOrigin: true, secure: false },
        "/ws": { target: wsTarget, ws: true, secure: false },
      },
    },
  }),
);
