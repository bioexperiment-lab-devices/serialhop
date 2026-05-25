# Changelog

## [0.31.4](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.31.3...v0.31.4) (2026-05-25)


### Bug Fixes

* **streamer:** log ffmpeg spawn with redacted argv and flag fast exits ([#163](https://github.com/bioexperiment-lab-devices/serialhop/issues/163)) ([c5e7397](https://github.com/bioexperiment-lab-devices/serialhop/commit/c5e7397f32babd30a79f1073f32f79e20f82948b))

## [0.31.3](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.31.2...v0.31.3) (2026-05-25)


### Bug Fixes

* **streamer:** pass raw bearer token to ffmpeg whip muxer; capture stderr tail ([#161](https://github.com/bioexperiment-lab-devices/serialhop/issues/161)) ([d4a675d](https://github.com/bioexperiment-lab-devices/serialhop/commit/d4a675d67836fa33d9a128e873d7db43c72abba2))

## [0.31.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.31.1...v0.31.2) (2026-05-25)


### Bug Fixes

* slugify DirectShow camera ids before announcing them ([#159](https://github.com/bioexperiment-lab-devices/serialhop/issues/159)) ([87600c8](https://github.com/bioexperiment-lab-devices/serialhop/commit/87600c860798954f7a66899cfb10a4317fb4dab5))

## [0.31.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.31.0...v0.31.1) (2026-05-25)


### Bug Fixes

* validate camera_id and session_id on Stop ([#157](https://github.com/bioexperiment-lab-devices/serialhop/issues/157)) ([ed47855](https://github.com/bioexperiment-lab-devices/serialhop/commit/ed47855d023ee8b5444b8205501cbb4ac33d3849))

## [0.31.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.30.0...v0.31.0) (2026-05-25)


### Features

* surface enumeration errors + add Cameras Diagnose button ([#153](https://github.com/bioexperiment-lab-devices/serialhop/issues/153)) ([a08e49a](https://github.com/bioexperiment-lab-devices/serialhop/commit/a08e49a8c313aefd395ff795c205380b3bd03b88))

## [0.30.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.29.0...v0.30.0) (2026-05-25)


### Features

* add camera streaming (SerialHop protocol v1) ([#150](https://github.com/bioexperiment-lab-devices/serialhop/issues/150)) ([3a5f1f2](https://github.com/bioexperiment-lab-devices/serialhop/commit/3a5f1f269f75d7193c5bbefb701de7e56db266fe))

## [0.29.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.28.0...v0.29.0) (2026-05-19)


### Features

* keep-awake button on Status tab (panel + service) ([#144](https://github.com/bioexperiment-lab-devices/serialhop/issues/144)) ([40cc476](https://github.com/bioexperiment-lab-devices/serialhop/commit/40cc476c827d6e9f7df8b7f0d7e4d6dfe66db53e))

## [0.28.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.27.1...v0.28.0) (2026-05-18)


### Features

* add GET /agent/info for server-pulled agent state ([#142](https://github.com/bioexperiment-lab-devices/serialhop/issues/142)) ([7394d56](https://github.com/bioexperiment-lab-devices/serialhop/commit/7394d56cfffcd95a0817626da77d9e9d15bac15b))

## [0.27.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.27.0...v0.27.1) (2026-05-17)


### Bug Fixes

* cap firmware backup at user space so it round-trips through /flash ([#138](https://github.com/bioexperiment-lab-devices/serialhop/issues/138)) ([4efc4ef](https://github.com/bioexperiment-lab-devices/serialhop/commit/4efc4ef320bf3a7b5a434e8f9635b66db3134b10))

## [0.27.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.26.0...v0.27.0) (2026-05-16)


### Features

* disconnect a single device by port + inline action ([#136](https://github.com/bioexperiment-lab-devices/serialhop/issues/136)) ([6ed73d4](https://github.com/bioexperiment-lab-devices/serialhop/commit/6ed73d48d18f5df7da6b1e60fdac6444e017ba3f))

## [0.26.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.25.2...v0.26.0) (2026-05-16)


### Features

* comprehensive logging for service + panel ([#134](https://github.com/bioexperiment-lab-devices/serialhop/issues/134)) ([abec359](https://github.com/bioexperiment-lab-devices/serialhop/commit/abec359a25f3f7ca905285ea158e618a6eb3ebfe))

## [0.25.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.25.1...v0.25.2) (2026-05-16)


### Bug Fixes

* **panel:** cache running lab-bridge identity for status-badge probes ([#132](https://github.com/bioexperiment-lab-devices/serialhop/issues/132)) ([b3b0b8d](https://github.com/bioexperiment-lab-devices/serialhop/commit/b3b0b8d08b65349482d04550857b1184d0f44010))

## [0.25.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.25.0...v0.25.1) (2026-05-16)


### Bug Fixes

* **config:** validate lab_bridge.host as IPv4 or RFC 1123 hostname ([#131](https://github.com/bioexperiment-lab-devices/serialhop/issues/131)) ([78e8a64](https://github.com/bioexperiment-lab-devices/serialhop/commit/78e8a64077089a704cce2a35f6a2962688591eb6))
* **panel:** allow clearing integer config fields ([#129](https://github.com/bioexperiment-lab-devices/serialhop/issues/129)) ([aff5b11](https://github.com/bioexperiment-lab-devices/serialhop/commit/aff5b11d5f4b74f2c10e596e88ec1dc06ea4ef68))

## [0.25.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.24.0...v0.25.0) (2026-05-15)


### Features

* **panel:** default save flow to Save & restart, footer hint after Save ([#127](https://github.com/bioexperiment-lab-devices/serialhop/issues/127)) ([ed7833f](https://github.com/bioexperiment-lab-devices/serialhop/commit/ed7833fc21b3129f1e2fb601df24623fdc421d93))
* **panel:** persistent update row + relaunch on install + ports filter ([#126](https://github.com/bioexperiment-lab-devices/serialhop/issues/126)) ([51494d4](https://github.com/bioexperiment-lab-devices/serialhop/commit/51494d4574838d67957bbd756cece9f3d0a7fb7c))

## [0.24.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.23.0...v0.24.0) (2026-05-15)


### Features

* **panel:** config validation + lamp sub lines + minor UI parity ([#124](https://github.com/bioexperiment-lab-devices/serialhop/issues/124)) ([d53740b](https://github.com/bioexperiment-lab-devices/serialhop/commit/d53740baf1f5446af8b92c5f29d28b6daf2800e6))

## [0.23.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.22.2...v0.23.0) (2026-05-15)


### Features

* **panel:** align panel UI with design handoff ([#122](https://github.com/bioexperiment-lab-devices/serialhop/issues/122)) ([bfd7813](https://github.com/bioexperiment-lab-devices/serialhop/commit/bfd781379014d6c632b68962d31a52996d0ad17d))

## [0.22.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.22.1...v0.22.2) (2026-05-15)


### Bug Fixes

* **panel:** stage auto-update downloads under %LOCALAPPDATA% ([#120](https://github.com/bioexperiment-lab-devices/serialhop/issues/120)) ([0949297](https://github.com/bioexperiment-lab-devices/serialhop/commit/094929754ae96e495ba976e962a9b6bfcbd20d98))

## [0.22.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.22.0...v0.22.1) (2026-05-15)


### Bug Fixes

* prevent Devices tab UI blank-out when service is not installed ([#114](https://github.com/bioexperiment-lab-devices/serialhop/issues/114)) ([222b80c](https://github.com/bioexperiment-lab-devices/serialhop/commit/222b80cc9ed5969d1e949f0a45d05f982c87cfc6))

## [0.22.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.21.0...v0.22.0) (2026-05-15)


### Chores

* force release 0.22.0 (Release-As trailer) ([#117](https://github.com/bioexperiment-lab-devices/serialhop/issues/117)) ([0a4b703](https://github.com/bioexperiment-lab-devices/serialhop/commit/0a4b703e4641f789fede80deb1abf9c23762bd24))

## [0.21.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.20.0...v0.21.0) (2026-05-15)


### Features

* panel crash safety net (error boundary + crash journal) ([#113](https://github.com/bioexperiment-lab-devices/serialhop/issues/113)) ([4d93400](https://github.com/bioexperiment-lab-devices/serialhop/commit/4d93400e8760dbd3938b8c34a96deb1ad329364a))


### Bug Fixes

* **panel:** apply design button variants and reposition log/update affordances ([#111](https://github.com/bioexperiment-lab-devices/serialhop/issues/111)) ([5f9701e](https://github.com/bioexperiment-lab-devices/serialhop/commit/5f9701e9794f63ca9e0fc71bc1163dc2f28c1eb8))

## [0.20.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.19.0...v0.20.0) (2026-05-15)


### Features

* **installer:** replace walk dialog with Wails-styled window; auto-close on launch ([#109](https://github.com/bioexperiment-lab-devices/serialhop/issues/109)) ([3f3cc86](https://github.com/bioexperiment-lab-devices/serialhop/commit/3f3cc86418d2e8917ee54ab0cf5ec4b64f0cc0a6))

## [0.19.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.18.3...v0.19.0) (2026-05-15)


### Features

* ship installer with unversioned desktop shortcut and in-place upgrade ([#107](https://github.com/bioexperiment-lab-devices/serialhop/issues/107)) ([e80924a](https://github.com/bioexperiment-lab-devices/serialhop/commit/e80924aa672a5214896fcc57adaab46059d0c9f5))

## [0.18.3](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.18.2...v0.18.3) (2026-05-14)


### Bug Fixes

* **panel:** drop ctx from Wails-bound methods — actual reachability fix ([#104](https://github.com/bioexperiment-lab-devices/serialhop/issues/104)) ([6399a67](https://github.com/bioexperiment-lab-devices/serialhop/commit/6399a6770d5d01b8b9ea413c6d91b448b2b979c6))

## [0.18.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.18.1...v0.18.2) (2026-05-14)


### Bug Fixes

* **flasher:** align STK500v1 flow with optiboot reality ([#102](https://github.com/bioexperiment-lab-devices/serialhop/issues/102)) ([193957d](https://github.com/bioexperiment-lab-devices/serialhop/commit/193957d5ac5cb579f6587c5a765e64bb92cba9a1))

## [0.18.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.18.0...v0.18.1) (2026-05-14)


### Bug Fixes

* **panel:** HTTP-probe Diagnostics + surface JS binding errors ([#100](https://github.com/bioexperiment-lab-devices/serialhop/issues/100)) ([301ba2a](https://github.com/bioexperiment-lab-devices/serialhop/commit/301ba2a8dbded06d4ddf60a4779a95f2182bbd73))

## [0.18.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.17.1...v0.18.0) (2026-05-14)


### Features

* **panel:** inline log detail + stretchable mono view + scroll fixes ([#97](https://github.com/bioexperiment-lab-devices/serialhop/issues/97)) ([a0281b1](https://github.com/bioexperiment-lab-devices/serialhop/commit/a0281b1ed252a5209a9ca64f3394556c6438f846))

## [0.17.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.17.0...v0.17.1) (2026-05-14)


### Bug Fixes

* **panel:** bypass cache user-anchor on reachability path + log reason ([#95](https://github.com/bioexperiment-lab-devices/serialhop/issues/95)) ([528cdfe](https://github.com/bioexperiment-lab-devices/serialhop/commit/528cdfee314fa6eb7339155c71f343bfe72610ab))

## [0.17.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.16.1...v0.17.0) (2026-05-14)


### Features

* **panel:** sticky logs filter bar + newest-first log order ([#92](https://github.com/bioexperiment-lab-devices/serialhop/issues/92)) ([a0ee2f6](https://github.com/bioexperiment-lab-devices/serialhop/commit/a0ee2f6c24f67db896713be8baa250726d832408))


### Bug Fixes

* **panel:** refresh status lamps after admin actions and on startup ([#93](https://github.com/bioexperiment-lab-devices/serialhop/issues/93)) ([a4b5d0b](https://github.com/bioexperiment-lab-devices/serialhop/commit/a4b5d0b1a64e77d783676ba12a181c5224dc1351))

## [0.16.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.16.0...v0.16.1) (2026-05-14)


### Bug Fixes

* **panel:** reachability after first-run + persistent logs with backlog ([#90](https://github.com/bioexperiment-lab-devices/serialhop/issues/90)) ([59b237e](https://github.com/bioexperiment-lab-devices/serialhop/commit/59b237e7229dbb65d0db6d1f431c3b02652007f4))

## [0.16.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.15.0...v0.16.0) (2026-05-14)


### Features

* **panel:** frameless window with custom titlebar buttons + sticky chrome ([#88](https://github.com/bioexperiment-lab-devices/serialhop/issues/88)) ([465b1dd](https://github.com/bioexperiment-lab-devices/serialhop/commit/465b1ddc44744aa0de88b93bb4de0b0f577eba49))

## [0.15.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.14.4...v0.15.0) (2026-05-14)


### Features

* **panel:** responsive layout with 720px collapse breakpoint ([#83](https://github.com/bioexperiment-lab-devices/serialhop/issues/83)) ([235e27f](https://github.com/bioexperiment-lab-devices/serialhop/commit/235e27fca0d3783df73b485837a503983b4582be))
* **panel:** vite dev preview with wails-shim for macOS ([#85](https://github.com/bioexperiment-lab-devices/serialhop/issues/85)) ([f657b49](https://github.com/bioexperiment-lab-devices/serialhop/commit/f657b49d0d4d9e9adb5a20452c277c17ad91702a))


### Bug Fixes

* **panel:** help popover hover + portal + viewport clamp ([#84](https://github.com/bioexperiment-lab-devices/serialhop/issues/84)) ([83b829e](https://github.com/bioexperiment-lab-devices/serialhop/commit/83b829ec26b90854427ab22b2d4de9eb8d5168ec))
* **panel:** reconcile tab class names to design tokens ([#82](https://github.com/bioexperiment-lab-devices/serialhop/issues/82)) ([0163199](https://github.com/bioexperiment-lab-devices/serialhop/commit/0163199ef14b5e97f65ebd4a28705e85812f2028))
* **panel:** remove faux window frame and fluid sizing ([#81](https://github.com/bioexperiment-lab-devices/serialhop/issues/81)) ([daebac1](https://github.com/bioexperiment-lab-devices/serialhop/commit/daebac1e6f8c69a66f3ae0aee9dd45b27aa2f6f3))

## [0.14.4](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.14.3...v0.14.4) (2026-05-14)


### Bug Fixes

* **panel:** bind App from package main so SPA finds window.go.main.App ([#77](https://github.com/bioexperiment-lab-devices/serialhop/issues/77)) ([c995bb8](https://github.com/bioexperiment-lab-devices/serialhop/commit/c995bb8f764fb4c1d6491f119eae0e0db9105c2c))

## [0.14.3](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.14.2...v0.14.3) (2026-05-14)


### Bug Fixes

* **panel:** enable WebView2 context menu so operators can Inspect Element ([#74](https://github.com/bioexperiment-lab-devices/serialhop/issues/74)) ([2f4cd7a](https://github.com/bioexperiment-lab-devices/serialhop/commit/2f4cd7af16f0c8f4edf3e33d4938dc3c8a6d0ca3))

## [0.14.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.14.1...v0.14.2) (2026-05-14)


### Bug Fixes

* **build:** pass -nopackage to wails build to avoid duplicate .rsrc sections ([#72](https://github.com/bioexperiment-lab-devices/serialhop/issues/72)) ([ca08882](https://github.com/bioexperiment-lab-devices/serialhop/commit/ca0888269f336ac851c26d0108be945db27c4cec))

## [0.14.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.14.0...v0.14.1) (2026-05-14)


### Bug Fixes

* **frontend:** make Wails binding stubs delegate to runtime globals ([#70](https://github.com/bioexperiment-lab-devices/serialhop/issues/70)) ([9ea0293](https://github.com/bioexperiment-lab-devices/serialhop/commit/9ea0293cf8b640118d8a500e717a5dcff47ad451))

## [0.14.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.13.0...v0.14.0) (2026-05-14)


### Features

* **panel:** rewrite UI from lxn/walk to Wails v2 + React ([#68](https://github.com/bioexperiment-lab-devices/serialhop/issues/68)) ([fbfc013](https://github.com/bioexperiment-lab-devices/serialhop/commit/fbfc013348f9acc96a096762d96524a3e2ce0767))

## [0.13.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.12.0...v0.13.0) (2026-05-13)


### Features

* **api:** add skip_backup option to POST /flash/{port} ([#66](https://github.com/bioexperiment-lab-devices/serialhop/issues/66)) ([9ec9162](https://github.com/bioexperiment-lab-devices/serialhop/commit/9ec9162e580554b321d64bf00a1515c5196102dd))

## [0.12.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.11.0...v0.12.0) (2026-05-12)


### Features

* **api:** remote firmware flashing ([#63](https://github.com/bioexperiment-lab-devices/serialhop/issues/63)) ([509c6ac](https://github.com/bioexperiment-lab-devices/serialhop/commit/509c6acc721d77b25cc66d16cabfa5b3684ccb36))

## [0.11.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.10.0...v0.11.0) (2026-05-12)


### Features

* **panel:** refresh status lamps on user actions ([#61](https://github.com/bioexperiment-lab-devices/serialhop/issues/61)) ([628dd1f](https://github.com/bioexperiment-lab-devices/serialhop/commit/628dd1f6be43c609577f33a9c073cd1d3b2e9f17))

## [0.10.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.9.1...v0.10.0) (2026-05-12)


### Features

* server-info bootstrap and first-run credentials dialog ([#59](https://github.com/bioexperiment-lab-devices/serialhop/issues/59)) ([d6c5d20](https://github.com/bioexperiment-lab-devices/serialhop/commit/d6c5d20ae323d2d39fa59f56a85bbb6e3c21ef1b))

## [0.9.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.9.0...v0.9.1) (2026-05-11)


### Bug Fixes

* **panel:** paint lamp dots via CustomWidget filled circle ([#57](https://github.com/bioexperiment-lab-devices/serialhop/issues/57)) ([dfb7d0e](https://github.com/bioexperiment-lab-devices/serialhop/commit/dfb7d0e06cff30eda9ef70323e1c078ff92cf61a))

## [0.9.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.8.0...v0.9.0) (2026-05-11)


### Features

* status lamps for service, lab-bridge server, and tunnel ([#55](https://github.com/bioexperiment-lab-devices/serialhop/issues/55)) ([fc18fb4](https://github.com/bioexperiment-lab-devices/serialhop/commit/fc18fb480d9da65e2d23d2daf77b2282f35d453e))

## [0.8.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.7.0...v0.8.0) (2026-05-11)


### Features

* relocate config and logs to %ProgramData%, add Open logs folder button ([#52](https://github.com/bioexperiment-lab-devices/serialhop/issues/52)) ([8e09c65](https://github.com/bioexperiment-lab-devices/serialhop/commit/8e09c659a8dcd771eec8e246681fd53b492eba17))


### Bug Fixes

* shipper drops records when ctx cancels mid-HTTP-post ([#54](https://github.com/bioexperiment-lab-devices/serialhop/issues/54)) ([e5263c4](https://github.com/bioexperiment-lab-devices/serialhop/commit/e5263c419559f25a85def2e4c650a676b5fb08d1))

## [0.7.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.6.1...v0.7.0) (2026-05-11)


### Features

* **api:** per-call post-open settle on raw serial command ([#50](https://github.com/bioexperiment-lab-devices/serialhop/issues/50)) ([0922195](https://github.com/bioexperiment-lab-devices/serialhop/commit/09221956817e39f4871cf43de46cf4b653363474))
* **discovery:** configurable post-open settle delay ([#48](https://github.com/bioexperiment-lab-devices/serialhop/issues/48)) ([b2d3095](https://github.com/bioexperiment-lab-devices/serialhop/commit/b2d30959d6bc7edf5614fc4a5a447bb123ee056a))
* in-app auto-update with SHA-256 verification ([#51](https://github.com/bioexperiment-lab-devices/serialhop/issues/51)) ([7eea174](https://github.com/bioexperiment-lab-devices/serialhop/commit/7eea17406e75dfae369243b83856e2714af9b623))

## [0.6.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.6.0...v0.6.1) (2026-05-11)


### Bug Fixes

* **discovery:** wait out Arduino bootloader before probing ([#46](https://github.com/bioexperiment-lab-devices/serialhop/issues/46)) ([caa7f1d](https://github.com/bioexperiment-lab-devices/serialhop/commit/caa7f1d38113167b12019f2109b9ccfc0d93bda4))

## [0.6.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.5.2...v0.6.0) (2026-05-10)


### Features

* **api:** raw serial port endpoints ([#44](https://github.com/bioexperiment-lab-devices/serialhop/issues/44)) ([cd1775c](https://github.com/bioexperiment-lab-devices/serialhop/commit/cd1775cb73fce8c9ea7f6f19444b6cfffbc87211))
* **discovery:** log sent probe bytes and reply per port ([#43](https://github.com/bioexperiment-lab-devices/serialhop/issues/43)) ([9b10318](https://github.com/bioexperiment-lab-devices/serialhop/commit/9b10318dfa337ae06b108dc046b8ea16991d0a07))


### Bug Fixes

* **logs:** emit command/response bytes as integer arrays ([#42](https://github.com/bioexperiment-lab-devices/serialhop/issues/42)) ([5333bd8](https://github.com/bioexperiment-lab-devices/serialhop/commit/5333bd8bfb5eb962d8209337eabf62c138682a37))
* **manifest:** drop unsupported Windows versions from supportedOS ([#39](https://github.com/bioexperiment-lab-devices/serialhop/issues/39)) ([aacb5f4](https://github.com/bioexperiment-lab-devices/serialhop/commit/aacb5f465f763486cff3ac648556afe68e57c1ff))
* **ui:** strip git-describe suffix from window title ([#41](https://github.com/bioexperiment-lab-devices/serialhop/issues/41)) ([30eec48](https://github.com/bioexperiment-lab-devices/serialhop/commit/30eec48c63662bd0fd802b572ae811716d7283b3))

## [0.5.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.5.1...v0.5.2) (2026-05-03)


### Bug Fixes

* **ci:** strip 'v' prefix from VPS upload version ([#33](https://github.com/bioexperiment-lab-devices/serialhop/issues/33)) ([1a8b9e6](https://github.com/bioexperiment-lab-devices/serialhop/commit/1a8b9e69a1efee39fa067d4afab7148e3cb55187))

## [0.5.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.5.0...v0.5.1) (2026-05-03)


### Bug Fixes

* cap HTTP request body size and set server timeouts ([#31](https://github.com/bioexperiment-lab-devices/serialhop/issues/31)) ([d1ba0e7](https://github.com/bioexperiment-lab-devices/serialhop/commit/d1ba0e79631102dd896693ac4ee538409717a92e))

## [0.5.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.4.3...v0.5.0) (2026-05-02)


### Features

* add MIT license ([#23](https://github.com/bioexperiment-lab-devices/serialhop/issues/23)) ([c55dea0](https://github.com/bioexperiment-lab-devices/serialhop/commit/c55dea01b0e6785a50b65eb2f0884bbe355be12c))

## [0.4.3](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.4.2...v0.4.3) (2026-05-02)


### Bug Fixes

* **ci:** clean version suffix on tagged builds + spec sync ([#20](https://github.com/bioexperiment-lab-devices/serialhop/issues/20)) ([cd79248](https://github.com/bioexperiment-lab-devices/serialhop/commit/cd792482d239ae1c94b094a143818847469fb9fe))

## [0.4.2](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.4.1...v0.4.2) (2026-05-02)


### Bug Fixes

* **ci:** replace Taskfile shell pipelines with Go programs (Windows compat) ([#18](https://github.com/bioexperiment-lab-devices/serialhop/issues/18)) ([8fd75de](https://github.com/bioexperiment-lab-devices/serialhop/commit/8fd75de670663042260730400df11c60c2dcfccf))

## [0.4.1](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.4.0...v0.4.1) (2026-05-02)


### Bug Fixes

* **ci:** use awk instead of sed in manifest task for Windows compat ([#16](https://github.com/bioexperiment-lab-devices/serialhop/issues/16)) ([2a19013](https://github.com/bioexperiment-lab-devices/serialhop/commit/2a190133b1e6d38946264bdb70e41ce609a99196))

## [0.4.0](https://github.com/bioexperiment-lab-devices/serialhop/compare/v0.3.0...v0.4.0) (2026-05-02)


### Features

* add --version flag to print build version and exit ([#13](https://github.com/bioexperiment-lab-devices/serialhop/issues/13)) ([07d6355](https://github.com/bioexperiment-lab-devices/serialhop/commit/07d63559bf8d762027e2b0e6c98877e91b95854c))


### Bug Fixes

* **ci:** fix release-please token and drop unupdatable integer fields ([#14](https://github.com/bioexperiment-lab-devices/serialhop/issues/14)) ([8031545](https://github.com/bioexperiment-lab-devices/serialhop/commit/8031545f9dd2ea373c42184a15d5412d35ba1b14))
