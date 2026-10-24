import type { CapacitorConfig } from '@capacitor/cli'

const config: CapacitorConfig = {
  appId: 'games.storio.relaydemo',
  appName: 'Relay Lab',
  webDir: '../_Build/dist-app',
  server: { androidScheme: 'https', hostname: 'localhost' },
}
export default config
