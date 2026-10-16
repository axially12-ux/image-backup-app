// Nuxt ทำงานเป็น SPA (ไม่มี SSR)
// `nuxt generate` ส่งไฟล์ไปที่ .output/public แล้ว scripts/sync-dist.mjs คัดลอกเข้า frontend/dist
// ซึ่ง Wails ฝังเข้าไปในแอป (//go:embed all:frontend/dist)
export default defineNuxtConfig({
  ssr: false,
  devtools: { enabled: false },
  telemetry: false,
  compatibilityDate: '2025-01-01',
  css: ['~/assets/css/main.css'],
  app: {
    baseURL: '/',
    head: {
      title: 'Image Backup',
      meta: [{ name: 'viewport', content: 'width=device-width, initial-scale=1' }]
    }
  },
  devServer: { host: 'localhost', port: 3000 }
})
