<p align="center">
  <picture>
    <source media="(max-width: 640px) and (prefers-reduced-motion: reduce)" srcset="docs/assets/readme/hero-mobile-zh.png?v=883d7ca5e9">
    <source media="(max-width: 640px)" srcset="docs/assets/readme/hero-mobile-zh.gif?v=4affebf3b4">
    <source media="(prefers-reduced-motion: reduce)" srcset="docs/assets/readme/hero-zh.png?v=4da4bac82b">
    <img src="docs/assets/readme/hero-zh.gif?v=c6ae642fa1" width="100%" alt="Apophis TeamSeatWatch：把 Team 管理变成清晰的日常。账号准备、空间席位、批次接入与卡密交付。">
  </picture>
</p>

<h1 align="center">Apophis TeamSeatWatch</h1>

<p align="center">
  面向临期 ChatGPT Team 的公益开源管理工作台。<br>
  账号、席位与交付，在一处清晰掌握。
</p>

<p align="center">
  简体中文 · <a href="README.en.md">English</a>
</p>

<p align="center">
  <a href="#preview">产品预览</a> &nbsp; / &nbsp;
  <a href="#workflow">使用流程</a> &nbsp; / &nbsp;
  <a href="#start">快速开始</a> &nbsp; / &nbsp;
  <a href="#community">社区</a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-4C4EDA?style=flat-square" alt="Apache 2.0 License"></a>
</p>

---

<p align="center"><strong>独家赞助商</strong></p>

<a href="https://www.apophis.uk/">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/apophiscode-light-mobile-zh.png?v=620bab4857">
    <img src="docs/assets/readme/apophiscode-light-zh.png?v=c2d9116048" width="100%" alt="ApophisCode 独家赞助：Claude Fable 5.1 满血 Max 1.4x；Claude Opus 5 Kiro 0.25x；GPT-6 Astra 0.1x。新用户进群绑定账号领额度，每日签到再领。赞助商 QQ 群 425421707。点击访问官网。">
  </picture>
</a>

<p align="center">
  <sub>活动文案由赞助商提供，模型、倍率与赠额活动以官网及赞助群实时信息为准。</sub>
</p>

<a id="preview"></a>

## 产品预览

从账号准备到卡密交付，在同一个工作台查看名单、处理进度与交付结果。

<a href="docs/assets/account-management.png">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/product-mobile-zh.png?v=6bf5d7fee2">
    <img src="docs/assets/readme/product-zh.png?v=077b7a8285" width="100%" alt="真实账号管理界面，使用虚构示例账号：查看状态、筛选账号、选中批量处理及执行行内操作。点击查看原图。">
  </picture>
</a>

<p align="center"><sub>截图使用虚构示例数据。应用界面目前以中文为主；README 与部署指南提供中英文。</sub></p>

- **账号与批次**：导入、筛选、检查账号状态，将待用账号整理成明确批次。
- **空间与接入**：查看订阅、成员与席位，按本轮名单完成邀请、授权登录和卡密生成。
- **交付与跟进**：复制或导出卡密，查询兑换与找回记录，核对后确认轮转。

<a id="workflow"></a>

## 使用流程

<picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/workflow-mobile-zh.png?v=b215771bc6">
    <img src="docs/assets/readme/workflow-zh.png?v=d0827d7e0d" width="100%" alt="产品架构与业务流程：管理员准备账号、核对空间席位、邀请和登录，生成一卡一号的卡密；用户通过独立入口兑换与找回；管理者查询记录并人工确认轮转，再准备下一批。">
  </picture>

1. **准备**：导入并检查账号，整理待用批次。
2. **接入**：选定空间与名单，完成邀请和授权登录。
3. **交付**：生成、复制或导出卡密，由用户在兑换入口领取。
4. **跟进**：查看兑换与找回记录，轮转前核对范围并确认。

> **一卡一号，多卡分别记录。** 401 找回继续使用原账号、原空间；轮转保留人工确认与既有交付保护。

<a id="start"></a>

## 快速开始

准备一台 **Linux x86_64** 主机，安装 Docker Engine（含 Compose）、Python 3 和 OpenSSL。执行：

```bash
git clone https://github.com/xft0202/Apophis-TeamSeatWatch.git
cd Apophis-TeamSeatWatch
python3 deploy/manage.py start
```

首次启动会自动构建应用、准备配置，并引导你设置管理员密码。看到 **“已就绪 / Ready”** 后，按提示信任本机证书，然后打开：

- **管理工作台**：`https://localhost:18443/owner/`<br>
  使用 `owner` 与刚设置的密码登录。
- **用户兑换入口**：`https://localhost:18444/redeem/`<br>
  用于卡密兑换与 401 找回。

**[查看部署与首次使用指南 →](docs/deployment.md)**<br>
以上为部署主机的本机入口，远程访问、兑换域名与日常维护方法见指南。

<a id="community"></a>

## 交流与反馈

<p>
<picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/community-exchange-mobile-zh.png?v=d2404a50ba">
    <img src="docs/assets/readme/community-exchange-zh.png?v=91a147685d" width="100%" alt="项目 QQ 交流群：1129084117。交流使用心得，分享日常管理经验；在 QQ 内搜索群号加入。">
  </picture>
</p>

<p>
<a href="https://github.com/xft0202/Apophis-TeamSeatWatch/issues">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/community-feedback-mobile-zh.png?v=123a87ec51">
    <img src="docs/assets/readme/community-feedback-zh.png?v=e962352973" width="100%" alt="问题与建议。记录遇到的问题，提出期待的改进。点击前往 GitHub Issues。">
  </picture>
</a>
</p>

<sub>附图请隐藏账号凭据等敏感信息。</sub>

---

<p align="center">
  <strong>Apophis TeamSeatWatch</strong><br>
  <sub>公益开源 · 自托管 · 欢迎参与改进</sub><br>
  <sub><a href="LICENSE">Apache License 2.0</a> · <a href="NOTICE">NOTICE</a> · 由 <a href="https://www.apophis.uk/">ApophisCode</a> 独家赞助</sub>
</p>
