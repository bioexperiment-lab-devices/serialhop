# Changelog

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
