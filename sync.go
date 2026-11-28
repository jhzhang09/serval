package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"serval/core/syncer"
	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

type syncGuideLink struct {
	Label string
	URL   string
}

type s3VendorMeta struct {
	Name                string
	DefaultRegion       string
	RegionPlaceholder   string
	BucketPlaceholder   string
	EndpointPlaceholder string
	EndpointHint        string
	AccessIDPlaceholder string
	SecretHint          string
	Summary             string
	Steps               []string
	Links               []syncGuideLink
}

var s3VendorMetas = []s3VendorMeta{
	{
		Name:                "AWS S3",
		DefaultRegion:       "us-east-1",
		RegionPlaceholder:   "如 us-east-1 / ap-southeast-1",
		BucketPlaceholder:   "存储桶名称（全局唯一）",
		EndpointPlaceholder: "AWS S3 可留空",
		EndpointHint:        "官方 AWS S3 留空即可，将按 Region 自动匹配区域地址。",
		AccessIDPlaceholder: "IAM 用户的 Access key ID（如 AKIA...）",
		SecretHint:          "填入对应 Secret access key（只保存在本机系统安全存储中）。",
		Summary:             "S3 私有桶 + IAM 子用户读写密钥；Endpoint 可留空。",
		Steps: []string{
			"1. 在 S3 控制台新建私有 Bucket，记下桶名和所在 Region（如 ap-southeast-1）。",
			"2. 到 IAM → 用户创建访问密钥（CLI 用途），授予该桶 GetObject/PutObject/HeadObject 权限。",
			"3. Endpoint 留空，分别填入 Region、Bucket、Access key ID 与 Secret access key。",
		},
		Links: []syncGuideLink{
			{Label: "打开 S3 控制台", URL: "https://s3.console.aws.amazon.com/"},
			{Label: "打开 IAM 控制台", URL: "https://console.aws.amazon.com/iam/"},
		},
	},
	{
		Name:                "Cloudflare R2",
		DefaultRegion:       "auto",
		RegionPlaceholder:   "固定填 auto",
		BucketPlaceholder:   "存储桶名称",
		EndpointPlaceholder: "https://<账户ID>.r2.cloudflarestorage.com",
		EndpointHint:        "在 R2 存储桶「设置」页复制“S3 API”地址（切勿填公开域名 r2.dev）。",
		AccessIDPlaceholder: "R2 API 令牌里的 Access Key ID",
		SecretHint:          "填入令牌创建页显示的 Secret Access Key（仅创建时展示一次）。",
		Summary:             "Region 填 auto；到「管理 R2 API 令牌」生成对象读写密钥与 S3 API 地址。",
		Steps: []string{
			"1. 打开 R2 控制台建一个 Bucket，进入「管理 R2 API 令牌 → 创建 API 令牌」。",
			"2. 权限选择「对象读与写」，创建后复制 Access Key ID、Secret Access Key 与管辖端点。",
			"3. Endpoint 填页面上提供的 S3 API 地址，Region 保持 auto，Bucket 填桶名。",
		},
		Links: []syncGuideLink{
			{Label: "打开 R2 控制台", URL: "https://dash.cloudflare.com/?to=/:account/r2"},
		},
	},
	{
		Name:                "MinIO",
		DefaultRegion:       "us-east-1",
		RegionPlaceholder:   "一般填 us-east-1",
		BucketPlaceholder:   "存储桶名称",
		EndpointPlaceholder: "例如 http://127.0.0.1:9000",
		EndpointHint:        "填写 MinIO S3 API 地址（通常为 9000 端口，而非控制台网页端口 9001）。",
		AccessIDPlaceholder: "MinIO Access Key",
		SecretHint:          "填入对应 Secret Key（仅保存在本机系统安全存储中）。",
		Summary:             "Endpoint 填 API 端口（如 9000）；在后台「Access Keys」生成读写密钥。",
		Steps: []string{
			"1. 登录 MinIO 控制台创建一个私有 Bucket。",
			"2. 左侧进入「Access Keys → Create access key」生成一组 Access Key 与 Secret Key。",
			"3. Endpoint 填 API 地址（默认 9000 端口），Region 保持 us-east-1 即可。",
		},
		Links: []syncGuideLink{
			{Label: "查看 MinIO 文档", URL: "https://min.io/docs/minio/linux/index.html"},
		},
	},
	{
		Name:                "阿里云 OSS",
		DefaultRegion:       "oss-cn-hangzhou",
		RegionPlaceholder:   "如 oss-cn-hangzhou（填 Endpoint 后自动识别）",
		BucketPlaceholder:   "存储桶名称",
		EndpointPlaceholder: "https://oss-cn-hangzhou.aliyuncs.com",
		EndpointHint:        "在 OSS Bucket「概览 → 访问域名」复制“外网访问 Endpoint”（不带桶名前缀）。",
		AccessIDPlaceholder: "RAM 用户的 AccessKey ID",
		SecretHint:          "对应阿里云的 AccessKey Secret（仅创建时展示，保存在系统安全存储中）。",
		Summary:             "填外网 Endpoint 后自动带出 oss-* Region；在 RAM 创建读写密钥。",
		Steps: []string{
			"1. 在 OSS 控制台新建“私有”读写 Bucket，在「概览」复制外网 Endpoint（填后自动推导 Region）。",
			"2. 到 RAM 访问控制创建子用户（OpenAPI 调用访问），授权目标桶读写（AliyunOSSFullAccess 或单桶策略）。",
			"3. 把子用户的 AccessKey ID 和 AccessKey Secret 分别填入下方两栏。",
		},
		Links: []syncGuideLink{
			{Label: "打开 OSS 控制台", URL: "https://oss.console.aliyun.com/"},
			{Label: "打开 RAM 访问控制", URL: "https://ram.console.aliyun.com/"},
		},
	},
	{
		Name:                "腾讯云 COS",
		DefaultRegion:       "ap-guangzhou",
		RegionPlaceholder:   "如 ap-guangzhou（填 Endpoint 后自动识别）",
		BucketPlaceholder:   "完整桶名（必须带 -AppID，如 serval-1250000000）",
		EndpointPlaceholder: "https://cos.ap-guangzhou.myqcloud.com",
		EndpointHint:        "在 COS 桶「概览」看访问域名，去掉前面桶名后的部分（如 https://cos.ap-guangzhou.myqcloud.com）。",
		AccessIDPlaceholder: "对应腾讯云 CAM 密钥里的 SecretId",
		SecretHint:          "对应腾讯云的 SecretKey（只创建时显示一次，保存在本机系统安全存储中）。",
		Summary:             "Bucket 需带 -AppID；Access Key ID 填 SecretId，Secret 填 SecretKey。",
		Steps: []string{
			"1. 在 COS 控制台建“私有读写”存储桶，桶名末尾必须保留 -AppID（如 serval-1250000000）。",
			"2. 到访问管理 CAM → 用户，新建「编程访问」子账号并授予 COS 读写权限，生成 SecretId 与 SecretKey。",
			"3. 填完 Endpoint 会自动填充 Region；Access Key ID 填 SecretId，Secret 栏填 SecretKey。",
		},
		Links: []syncGuideLink{
			{Label: "打开 COS 控制台", URL: "https://console.cloud.tencent.com/cos"},
			{Label: "打开 CAM 访问管理", URL: "https://console.cloud.tencent.com/cam"},
		},
	},
	{
		Name:                "华为云 OBS",
		DefaultRegion:       "cn-north-4",
		RegionPlaceholder:   "如 cn-north-4（填 Endpoint 后自动识别）",
		BucketPlaceholder:   "存储桶名称",
		EndpointPlaceholder: "https://obs.cn-north-4.myhuaweicloud.com",
		EndpointHint:        "在 OBS 桶「概览 → 基本信息」复制 Endpoint（不带桶名前缀）。",
		AccessIDPlaceholder: "华为云「我的凭证」里的 AK（Access Key ID）",
		SecretHint:          "对应「我的凭证」下载文件里的 SK（Secret Access Key）。",
		Summary:             "填 Endpoint 自动带出 Region；在「我的凭证」获取 AK 和 SK。",
		Steps: []string{
			"1. 在 OBS 控制台创建“私有”存储桶，在「概览」复制 Endpoint（粘贴后自动推导 Region）。",
			"2. 右上角账号 →「我的凭证 → 访问密钥」新增访问密钥（或使用授权 OBS 读写的 IAM 子用户 AK/SK）。",
			"3. 将下载到的 Access Key Id (AK) 与 Secret Access Key (SK) 填入下方。",
		},
		Links: []syncGuideLink{
			{Label: "打开 OBS 控制台", URL: "https://console.huaweicloud.com/obs/"},
			{Label: "打开我的凭证", URL: "https://console.huaweicloud.com/home/?locale=zh-cn#/kps/myCredential"},
		},
	},
	{
		Name:                "S3 兼容服务（自定义）",
		DefaultRegion:       "us-east-1",
		RegionPlaceholder:   "服务商要求填写的 Region，一般为 us-east-1",
		BucketPlaceholder:   "存储桶名称",
		EndpointPlaceholder: "https://s3.your-provider.com",
		EndpointHint:        "填写第三方对象存储提供的 S3 API 终端地址。",
		AccessIDPlaceholder: "访问密钥 ID（Access Key ID）",
		SecretHint:          "填入对应的访问密钥（Secret Access Key），只保存在系统安全存储中。",
		Summary:             "支持 Backblaze B2、Wasabi、Ceph RGW 等 S3 SigV4 兼容存储。",
		Steps: []string{
			"1. 适用于 Backblaze B2、Wasabi、Ceph RGW、Garage 等兼容 S3 SigV4 协议的存储。",
			"2. 在对应后台新建私有 Bucket，获取具备对象读写权限的 Key ID、Secret 与 Endpoint。",
		},
	},
}

func s3MetaFor(vendor string) s3VendorMeta {
	for _, m := range s3VendorMetas {
		if m.Name == vendor {
			return m
		}
	}
	return s3VendorMetas[0]
}

// inferS3Region 尝试从常见云厂商的 Endpoint 域名中自动提取 Region，降低配置出错率。
func inferS3Region(vendor, endpoint string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ""
	}
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, ".r2.cloudflarestorage.com"):
		return "auto"
	case strings.HasSuffix(host, ".aliyuncs.com"):
		first, _, _ := strings.Cut(host, ".")
		if strings.HasPrefix(first, "oss-") {
			return first
		}
	case strings.HasSuffix(host, ".myqcloud.com"):
		parts := strings.Split(host, ".")
		for _, part := range parts {
			if strings.HasPrefix(part, "ap-") || strings.HasPrefix(part, "na-") || strings.HasPrefix(part, "eu-") || strings.HasPrefix(part, "sa-") {
				return part
			}
		}
	case strings.HasSuffix(host, ".myhuaweicloud.com"):
		parts := strings.Split(host, ".")
		if len(parts) >= 3 && parts[0] == "obs" {
			return parts[1]
		}
	case strings.HasSuffix(host, ".amazonaws.com"):
		parts := strings.Split(host, ".")
		if len(parts) >= 4 && parts[0] == "s3" && parts[1] != "amazonaws" {
			return parts[1]
		}
	}
	if vendor == "Cloudflare R2" {
		return "auto"
	}
	return ""
}

type webdavFieldKeys struct {
	vendor, server, user, password, subDir string
}

type s3FieldKeys struct {
	vendor, region, bucket, accessID, secret, endpoint, subDir string
}

func (a *app) syncGuideCard(c *ui.Context, colors design.Colors, keyPrefix, summary string, expanded *bool, steps []string, links []syncGuideLink) {
	t := c.Theme()
	ui.Column(c).Padding(8, 10).Gap(6).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
		ui.Text(c, summary).FontSize(design.TextSmall).TextColor(t.TextMuted)
		if len(steps) > 0 || len(links) > 0 {
			ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
				if len(steps) > 0 && expanded != nil {
					label := "查看配置步骤"
					if *expanded {
						label = "收起配置步骤"
					}
					stepBtn := ui.ButtonBase(c.Key(keyPrefix+"GuideToggle")).Label(label).
						Height(22).Padding(0, 8).Radius(design.RadiusRow).
						Background(colors.Chip).AlignItems(ui.Center).Transition(design.Hover)
					if stepBtn.Hovered() {
						stepBtn.Background(t.Accent.Alpha(0.12))
					}
					stepBtn.Children(func() {
						ui.Text(c, label).FontSize(design.TextSmall).FontWeight(500).TextColor(t.Text).NoWrap()
					}).OnClick(func() {
						*expanded = !*expanded
					})
				}
				for i, item := range links {
					link := item
					btn := ui.ButtonBase(c.Key(fmt.Sprintf("%sGuideLink:%d", keyPrefix, i))).
						Label(link.Label).Height(22).Padding(0, 8).Gap(4).Radius(design.RadiusRow).
						Background(t.Accent.Alpha(0.12)).AlignItems(ui.Center).Transition(design.Hover)
					if btn.Hovered() {
						btn.Background(t.Accent.Alpha(0.20))
					}
					btn.Children(func() {
						ui.Icon(c, design.Icon("external-link")).FontSize(12).TextColor(t.Accent)
						ui.Text(c, link.Label).FontSize(design.TextSmall).FontWeight(500).TextColor(t.Accent).NoWrap()
					}).OnClick(func() {
						if a.openURL != nil {
							_ = a.openURL(link.URL)
						}
					})
				}
			})
		}
		if expanded != nil && *expanded {
			for _, step := range steps {
				ui.Text(c, step).FontSize(design.TextSmall).TextColor(t.TextMuted)
			}
		}
	})
}

func (a *app) webdavConfigFields(c *ui.Context, colors design.Colors, keys webdavFieldKeys,
	vendor, server, user, password *string, showPassword *bool, subDir *string, showGuide *bool,
	passwordPlaceholder, subDirPlaceholder string, fillDefaultDir bool,
) {
	if !slices.Contains(webdavVendors, *vendor) {
		*vendor = "自定义"
	}
	ui.Field(c, "服务商", func() {
		c.Key(keys.vendor)
		ui.Select(c, vendor, webdavVendors)
	})
	placeholder := "https://example.com/dav/"
	summary := "填写 WebDAV 目录地址与账号密码即可，无需手动写 /vault.json。"
	var steps []string
	var links []syncGuideLink
	switch *vendor {
	case "坚果云":
		placeholder = "https://dav.jianguoyun.com/dav/"
		if *server == "" {
			*server = "https://dav.jianguoyun.com/dav/"
		}
		summary = "坚果云请到「安全选项 → 第三方应用管理」生成专用应用密码（勿填登录密码）。"
		steps = []string{
			"1. 登录坚果云网页端，点右上角账号 →「账户信息 → 安全选项 → 第三方应用管理」。",
			"2. 添加应用（名称填 Serval）生成专用应用密码，将邮箱与该应用密码填入下方。",
		}
		links = []syncGuideLink{{Label: "打开坚果云官网", URL: "https://www.jianguoyun.com/"}}
	case "Nextcloud":
		placeholder = "https://your-domain.com/remote.php/dav/files/username/"
		summary = "在 Nextcloud「文件设置」左下角复制 WebDAV 地址；开两步验证需用应用密码。"
		steps = []string{
			"1. 打开 Nextcloud「文件」页面，在左下角「文件设置」里复制你的 WebDAV 地址。",
			"2. 若开启了两步验证，需到「个人设置 → 安全 → 设备与应用」创建专用应用密码。",
		}
	case "群晖 NAS":
		placeholder = "https://nas-ip:5006/home/"
		summary = "需在套件中心启用 WebDAV Server，建议使用 HTTPS 5006 端口。"
		steps = []string{
			"1. 在 DSM「套件中心」安装并启动 WebDAV Server，启用 HTTPS 端口（默认 5006）。",
			"2. 地址通常为 https://你的NAS地址:5006/home/，建议单独开一个同步用权限账号。",
		}
	}
	if summary != "" {
		a.syncGuideCard(c, colors, keys.vendor, summary, showGuide, steps, links)
	}

	ui.Field(c, "WebDAV 服务器地址", func() {
		c.Key(keys.server)
		ui.TextInput(c, server).Placeholder(placeholder)
	}).Description("填服务器目录地址即可，无需手动写 /vault.json。")

	ui.Field(c, "WebDAV 账户", func() {
		c.Key(keys.user)
		ui.TextInput(c, user).Placeholder("账号或邮箱")
	})

	ui.Field(c, "WebDAV 密码", func() {
		passwordInput(c, colors, keys.password, "WebDAV 密码", password, showPassword, func(el ui.Element) {
			el.Placeholder(passwordPlaceholder)
		})
	}).Description("凭据只保存在这台设备的系统安全存储中。")

	dirDesc := "数据将保存在该目录下（例如 serval/vault.json），便于多应用分类管理。"
	if !fillDefaultDir {
		dirDesc = "如另一台设备保留默认子目录 serval 可填 serval；若直接存在服务器根目录请留空。"
	}
	ui.Field(c, "远程根目录", func() {
		c.Key(keys.subDir)
		ui.TextInput(c, subDir).Placeholder(subDirPlaceholder)
	}).Description(dirDesc)
}

func (a *app) s3ConfigFields(c *ui.Context, colors design.Colors, keys s3FieldKeys,
	vendor, lastVendor, lastEndpoint, region, bucket, accessID, secret *string, showSecret *bool,
	endpoint, subDir *string, showGuide *bool, secretPlaceholder string,
) {
	if !slices.Contains(s3Vendors, *vendor) {
		*vendor = "AWS S3"
	}
	ui.Field(c, "服务商", func() {
		c.Key(keys.vendor)
		ui.Select(c, vendor, s3Vendors)
	})
	meta := s3MetaFor(*vendor)
	if *lastVendor != *vendor {
		prevMeta := s3MetaFor(*lastVendor)
		if *region == "" || *region == prevMeta.DefaultRegion {
			*region = meta.DefaultRegion
		}
		*lastVendor = *vendor
	}
	if *region == "" && meta.DefaultRegion != "" {
		*region = meta.DefaultRegion
	}
	if *endpoint != *lastEndpoint {
		prevInferred := inferS3Region(*vendor, *lastEndpoint)
		if inferred := inferS3Region(*vendor, *endpoint); inferred != "" &&
			(*region == "" || *region == meta.DefaultRegion || *region == prevInferred) {
			*region = inferred
		}
		*lastEndpoint = *endpoint
	}

	a.syncGuideCard(c, colors, keys.vendor, meta.Summary, showGuide, meta.Steps, meta.Links)

	ui.Field(c, "Endpoint 终端节点", func() {
		c.Key(keys.endpoint)
		ui.TextInput(c, endpoint).Placeholder(meta.EndpointPlaceholder)
	}).Description(meta.EndpointHint)

	ui.Field(c, "区域 (Region)", func() {
		c.Key(keys.region)
		ui.TextInput(c, region).Placeholder(meta.RegionPlaceholder)
	})

	ui.Field(c, "存储桶 (Bucket)", func() {
		c.Key(keys.bucket)
		ui.TextInput(c, bucket).Placeholder(meta.BucketPlaceholder)
	})

	ui.Field(c, "Access Key ID", func() {
		c.Key(keys.accessID)
		ui.TextInput(c, accessID).Placeholder(meta.AccessIDPlaceholder)
	})

	secretDesc := meta.SecretHint
	if secretDesc == "" {
		secretDesc = "密钥只保存在这台设备的系统安全存储中。"
	}
	ui.Field(c, "Secret Access Key", func() {
		passwordInput(c, colors, keys.secret, "Secret Access Key", secret, showSecret, func(el ui.Element) {
			el.Placeholder(secretPlaceholder)
		})
	}).Description(secretDesc)

	ui.Field(c, "远程根目录", func() {
		c.Key(keys.subDir)
		ui.TextInput(c, subDir).Placeholder("默认 serval")
	}).Description("数据将保存在该目录下（例如 serval/vault.json），多设备恢复时请保持一致。")
}

// syncSecretName 是系统安全存储里 WebDAV 密码的名字。地址和账号在 device.json，密码不进文件。
const syncSecretName = "sync-password"

// 自动同步的默认节奏。测试可以改 app.syncDelay 和 app.syncEvery。
const (
	defaultSyncDelay = 3 * time.Second
	defaultSyncEvery = 5 * time.Minute
)

func (a *app) syncConfigured() bool {
	return a.local.SyncOn && strings.TrimSpace(a.local.SyncURL) != ""
}

func (a *app) vaultOpen() bool {
	return a.vault != nil && a.vault.State() != vault.Locked
}

// normalizeSyncURL 规范化用户填写的 WebDAV 或 S3 同步目标地址。
func normalizeSyncURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("请填写同步地址")
	}
	if syncer.IsS3URL(s) {
		target, err := syncer.ParseS3URL(s)
		if err != nil {
			return "", err
		}
		return target.FormatURL(), nil
	}
	return syncer.FileURL(s)
}

// newSyncRemote 根据地址与凭据构造 Remote 客户端。
func newSyncRemote(fileURL, user, password string) (syncer.Remote, error) {
	if syncer.IsS3URL(fileURL) {
		return syncer.NewS3ClientFromURL(fileURL, user, password)
	}
	return &syncer.Client{URL: fileURL, User: user, Password: password}, nil
}

// probeSyncRemote 测试远端是否能连接。
func probeSyncRemote(ctx context.Context, fileURL, user, password string) error {
	remote, err := newSyncRemote(fileURL, user, password)
	if err != nil {
		return err
	}
	if probeable, ok := remote.(interface{ Probe(context.Context) error }); ok {
		return probeable.Probe(ctx)
	}
	_, _, err = remote.Download(ctx)
	if errors.Is(err, syncer.ErrNotFound) {
		return nil
	}
	return err
}

// buildSyncURLFromDraft 根据草稿面板字段构建规范的同步地址及账号、密码。
func (p *settingsPanel) buildDraftConfig() (fileURL string, user string, password string, err error) {
	subDir := strings.TrimSpace(p.syncSubDir)
	kind := strings.TrimSpace(p.syncKind)
	if kind == "S3 兼容存储" || syncer.IsS3URL(p.syncURL) {
		bucket := strings.TrimSpace(p.s3Bucket)
		if bucket == "" {
			return "", "", "", errors.New("请填写存储桶 (Bucket)")
		}
		cleanSub := strings.Trim(subDir, "/")
		key := "vault.json"
		if cleanSub != "" {
			key = cleanSub + "/vault.json"
		}
		target := syncer.S3Target{
			Endpoint: strings.TrimSpace(p.s3Endpoint),
			Region:   strings.TrimSpace(p.s3Region),
			Bucket:   bucket,
			Key:      key,
		}
		fileURL = target.FormatURL()
		user = strings.TrimSpace(p.s3AccessID)
		password = p.s3Secret
		return fileURL, user, password, nil
	}

	// WebDAV
	server := strings.TrimSpace(p.webdavServer)
	if server == "" {
		server = strings.TrimSpace(p.syncURL)
	}
	if server == "" {
		return "", "", "", errors.New("请填写 WebDAV 服务器地址")
	}
	fileURL, err = syncer.FileURLWithDir(server, subDir)
	if err != nil {
		return "", "", "", err
	}
	user = strings.TrimSpace(p.webdavUser)
	if user == "" {
		user = strings.TrimSpace(p.syncUser)
	}
	password = p.webdavPassword
	if password == "" {
		password = p.syncPassword
	}
	return fileURL, user, password, nil
}

func (a *app) configureSyncWithMeta(rawURL, user, password string, enabled bool, kind, vendor, subDir string) error {
	var fileURL string
	if strings.TrimSpace(rawURL) != "" {
		var err error
		fileURL, err = normalizeSyncURL(rawURL)
		if err != nil {
			return err
		}
	} else if enabled {
		return errors.New("请填写同步地址")
	}
	if password != "" {
		if !a.deviceAvailable() {
			return errors.New("当前系统未提供安全存储（Secret Service / 凭据管理器），无法保存同步密码")
		}
		if err := a.device.Set(syncSecretName, []byte(password)); err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				a.deviceSupported = false
				return errors.New("当前系统未提供安全存储（Secret Service / 凭据管理器），无法保存同步密码")
			}
			return errors.New("没能把同步密码放进系统安全存储")
		}
		a.syncPassCached = true
		a.syncPassValue = password
		a.syncPassDev = a.device
	}
	next := a.local
	next.SyncURL = fileURL
	next.SyncUser = strings.TrimSpace(user)
	next.SyncOn = enabled
	next.SyncSubDir = strings.TrimSpace(subDir)
	next.SyncVendor = strings.TrimSpace(vendor)
	next.SyncKind = strings.TrimSpace(kind)
	if err := a.saveLocal(next); err != nil {
		return err
	}
	if enabled && a.vaultOpen() && a.post != nil {
		a.onVaultReady()
	} else if !enabled {
		a.stopSyncLoop()
	}
	return nil
}

// configureSync 保存本机的同步设置。password 为空时保留安全存储里已有的密码。
// 地址会规范成 vault.json 的 URL。开启并且保险库未锁定、窗口已就绪时立刻同步一次。
func (a *app) configureSync(rawURL, user, password string, enabled bool) error {
	return a.configureSyncWithMeta(rawURL, user, password, enabled,
		a.settingsPanel.syncKind, a.settingsPanel.syncVendor, a.settingsPanel.syncSubDir)
}

// saveSyncSettings 把设置页里的草稿写入本机。密码留空时不覆盖系统安全存储里已有的密码。
// 已有数据的设备第一次开启同步时，先说明会按记录合并，不会整份覆盖。
func (a *app) saveSyncSettings() {
	p := &a.settingsPanel
	p.probeMsg = ""
	if p.syncOn && !a.local.SyncOn && a.vault != nil && !a.vault.Empty() && !p.allowMerge {
		p.askSyncMerge = true
		return
	}
	p.allowMerge = false

	fileURL, user, password, err := p.buildDraftConfig()
	if err != nil && p.syncOn {
		p.err = err.Error()
		return
	}
	if err == nil {
		p.syncURL = fileURL
		p.syncUser = user
		if password != "" {
			p.syncPassword = password
		}
	}

	if err := a.configureSync(p.syncURL, p.syncUser, p.syncPassword, p.syncOn); err != nil {
		p.err = err.Error()
		return
	}
	p.err = ""
	p.syncPassword = ""
	p.webdavPassword = ""
	p.s3Secret = ""
	p.showSyncPassword = false
	p.showS3Secret = false
	p.syncURL = a.local.SyncURL
	p.syncUser = a.local.SyncUser
	p.syncOn = a.local.SyncOn
	a.toast(ui.Toast{Title: "已保存同步设置"})
}

// commitSyncDraft 在离开同步分页或关闭设置面板时，若用户填写/修改了有效的同步配置草稿则自动持久化。
// 若从关闭改为开启同步且本机保险库已有数据、尚未确认合并，仅保存服务器与凭据草稿而不跳过合并确认。
func (a *app) commitSyncDraft() {
	p := &a.settingsPanel
	if p.askSyncMerge {
		return
	}
	fileURL, user, password, err := p.buildDraftConfig()
	if err != nil {
		return
	}
	user = strings.TrimSpace(user)
	wantOn := p.syncOn
	if wantOn && !a.local.SyncOn && a.vault != nil && !a.vault.Empty() && !p.allowMerge {
		wantOn = false
	}
	changed := fileURL != a.local.SyncURL ||
		user != a.local.SyncUser ||
		password != "" ||
		wantOn != a.local.SyncOn ||
		(a.local.SyncVendor != "" && strings.TrimSpace(p.syncVendor) != a.local.SyncVendor) ||
		(a.local.SyncKind != "" && strings.TrimSpace(p.syncKind) != a.local.SyncKind) ||
		(a.local.SyncURL != "" && strings.TrimSpace(p.syncSubDir) != a.local.SyncSubDir)
	if !changed {
		return
	}
	if err := a.configureSync(fileURL, user, password, wantOn); err != nil {
		p.err = err.Error()
		return
	}
	p.syncURL = a.local.SyncURL
	p.syncUser = a.local.SyncUser
	p.syncOn = a.local.SyncOn
	p.syncPassword = ""
	p.webdavPassword = ""
	p.s3Secret = ""
	p.showSyncPassword = false
	p.showS3Secret = false
}

// testSyncFromPanel 用当前草稿试一次连接，不保存、不上传。密码留空时用已保存的密码。
func (a *app) testSyncFromPanel() {
	p := &a.settingsPanel
	if p.probeBusy {
		return
	}
	fileURL, user, password, err := p.buildDraftConfig()
	if err != nil {
		// 回退兼容老字段
		rawURL := strings.TrimSpace(p.syncURL)
		if rawURL != "" {
			fileURL, err = normalizeSyncURL(rawURL)
			user = strings.TrimSpace(p.syncUser)
			password = p.syncPassword
		}
	}
	if err != nil {
		p.probeOK = false
		p.probeMsg = err.Error()
		p.err = ""
		return
	}
	if password == "" {
		password = a.syncPassword()
	}
	if a.post == nil {
		p.probeOK = false
		p.probeMsg = "窗口还没准备好，稍后再试。"
		return
	}
	p.probeBusy = true
	p.probeOK = false
	p.probeMsg = ""
	p.err = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := probeSyncRemote(ctx, fileURL, user, password)
		msg := "可以连接。"
		ok := err == nil
		if err != nil {
			msg = syncUserMessage(err)
		}
		a.post(func() {
			if !a.settingsPanel.open {
				return
			}
			a.settingsPanel.probeBusy = false
			a.settingsPanel.probeOK = ok
			a.settingsPanel.probeMsg = msg
		})
	}()
}

func formatSyncTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

func (a *app) openRestore() {
	a.settingsPanel = settingsPanel{}
	a.restore = restorePanel{
		open:         true,
		fresh:        true,
		syncKind:     "WebDAV",
		syncVendor:   "自定义",
		lastS3Vendor: "AWS S3",
		s3Region:     "us-east-1",
	}
}

// restoreDialog 让空设备下载已有的保险库，再用原来的主密码解锁。
func (a *app) restoreDialog(c *ui.Context, colors design.Colors) {
	p := &a.restore
	if !p.open || a.setup.open {
		return
	}
	if p.syncKind == "" {
		p.syncKind = "WebDAV"
	}
	_, windowHeight := c.Size()
	sheet(c, colors, &p.open, &p.fresh, 520, 22, func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Text(c, "恢复保险库").FontSize(design.TextHeading).FontWeight(600)
			ui.Text(c, "选择你在原设备使用的同步服务；下载后输入原来的主密码即可解锁全部主机与密钥。").
				FontSize(design.TextSmall).TextColor(c.Theme().TextMuted)
			ui.Field(c, "同步方式", func() {
				c.Key("restoreKind")
				ui.Select(c, &p.syncKind, syncKindOptions)
			})
			ui.Scroll(c.Key("restoreScroll")).MaxHeight(max(windowHeight-240, 220)).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					if p.syncKind == "S3 兼容存储" {
						if p.syncSubDir == "" && p.lastS3Vendor == "AWS S3" && p.s3Bucket == "" && p.s3Endpoint == "" {
							p.syncSubDir = "serval"
						}
						a.s3ConfigFields(c, colors, s3FieldKeys{
							vendor:   "restoreS3Vendor",
							region:   "restoreS3Region",
							bucket:   "restoreS3Bucket",
							accessID: "restoreS3AccessID",
							secret:   "restoreS3Secret",
							endpoint: "restoreS3Endpoint",
							subDir:   "restoreS3SubDir",
						}, &p.syncVendor, &p.lastS3Vendor, &p.lastS3Endpoint,
							&p.s3Region, &p.s3Bucket, &p.user, &p.password, &p.showPassword, &p.s3Endpoint, &p.syncSubDir,
							&p.showSyncGuide, "输入 Secret Access Key")
					} else {
						a.webdavConfigFields(c, colors, webdavFieldKeys{
							vendor:   "restoreWebdavVendor",
							server:   "restoreURL",
							user:     "restoreUser",
							password: "restorePassword",
							subDir:   "restoreSubDir",
						}, &p.syncVendor, &p.url, &p.user, &p.password, &p.showPassword, &p.syncSubDir,
							&p.showSyncGuide, "输入同步密码（坚果云填第三方应用密码）", "通常为 serval，若直接存在根目录可留空", false)
					}
				})
			})
			if p.err != "" {
				ui.Text(c.Key("restoreError"), p.err).FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				ui.Button(c.Key("restoreCancel"), "取消").Disabled(p.busy).OnClick(func() {
					a.restore = restorePanel{}
				})
				label := "开始恢复"
				if p.busy {
					label = "正在恢复…"
				}
				ui.PrimaryButton(c.Key("restoreSubmit"), label).Disabled(p.busy).OnClick(a.submitRestore)
			})
		})
	})
	if !p.open {
		a.restore = restorePanel{}
	}
}

// syncMergeDialog 在已有数据的设备开启同步前说明合并规则。默认按钮是取消。
func (a *app) syncMergeDialog(c *ui.Context) {
	p := &a.settingsPanel
	if !p.askSyncMerge {
		return
	}
	choice := ui.AlertDialog(c, &p.askSyncMerge, "与已有数据合并？",
		"这台设备上已经有数据。同步会按记录合并，不会用一边覆盖另一边。",
		"合并并同步", "取消")
	if choice == 0 {
		p.allowMerge = true
		a.saveSyncSettings()
	}
}

// syncOverwriteDialog 在远端是另一个保险库时询问是否覆盖。默认按钮是取消。
func (a *app) syncOverwriteDialog(c *ui.Context) {
	if !a.syncOverwriteAsk {
		return
	}
	choice := ui.AlertDialog(c, &a.syncOverwriteAsk, "用本机数据覆盖远端？",
		"远端已存在属于另一个保险库的文件（可能是历史测试遗留数据）。覆盖后远端历史文件将被替换为当前设备的数据。",
		"覆盖远端", "取消")
	if choice == 0 {
		a.overwriteRemote()
	}
}

// overwriteRemote 用当前本地的保险库强制覆盖远端。
func (a *app) overwriteRemote() {
	if !a.vaultOpen() || !a.syncConfigured() {
		return
	}
	v := a.vault
	fileURL, user, pass := a.local.SyncURL, a.local.SyncUser, a.syncPassword()
	a.syncing = true
	a.syncErr = ""
	a.syncDifferentVault = false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		remote, err := newSyncRemote(fileURL, user, pass)
		if err != nil {
			a.post(func() {
				a.syncing = false
				a.syncErr = syncUserMessage(err)
			})
			return
		}
		local, err := v.Bytes()
		if err != nil {
			a.post(func() {
				a.syncing = false
				a.syncErr = err.Error()
			})
			return
		}
		_, etag, _ := remote.Download(ctx)
		_, err = remote.Upload(ctx, local, etag)
		a.post(func() {
			a.syncing = false
			if !a.vaultOpen() || a.vault != v {
				return
			}
			if err != nil {
				a.syncErr = syncUserMessage(err)
				a.toast(ui.Toast{Title: "覆盖失败", Description: a.syncErr, Type: "error"})
			} else {
				a.syncErr = ""
				a.syncDifferentVault = false
				a.lastSync = time.Now()
				a.reload()
				a.toast(ui.Toast{Title: "已用本机覆盖远端"})
			}
		})
	}()
}

func (p *restorePanel) buildRestoreTarget() (fileURL, fallbackURL, kind, vendor, subDir string, err error) {
	rawURL := strings.TrimSpace(p.url)
	subDir = strings.TrimSpace(p.syncSubDir)
	kind = strings.TrimSpace(p.syncKind)
	if kind == "" {
		kind = "WebDAV"
	}
	vendor = strings.TrimSpace(p.syncVendor)

	// 兼容直接在 url 填入 s3://... 的快捷恢复或测试场景
	if syncer.IsS3URL(rawURL) && strings.TrimSpace(p.s3Bucket) == "" {
		fileURL, err = normalizeSyncURL(rawURL)
		if err != nil {
			return "", "", "", "", "", err
		}
		target, parseErr := syncer.ParseS3URL(fileURL)
		if parseErr == nil {
			if dir := path.Dir(target.Key); dir != "." && dir != "/" {
				subDir = dir
			} else {
				subDir = ""
			}
		}
		return fileURL, "", "S3 兼容存储", vendor, subDir, nil
	}

	if kind == "S3 兼容存储" {
		bucket := strings.TrimSpace(p.s3Bucket)
		if bucket == "" {
			return "", "", "", "", "", errors.New("请填写存储桶 (Bucket)")
		}
		cleanSub := strings.Trim(subDir, "/")
		key := "vault.json"
		if cleanSub != "" {
			key = cleanSub + "/vault.json"
		}
		reg := strings.TrimSpace(p.s3Region)
		if reg == "" {
			if inferred := inferS3Region(vendor, p.s3Endpoint); inferred != "" {
				reg = inferred
			} else {
				reg = s3MetaFor(vendor).DefaultRegion
			}
		}
		target := syncer.S3Target{
			Endpoint: strings.TrimSpace(p.s3Endpoint),
			Region:   reg,
			Bucket:   bucket,
			Key:      key,
		}
		fileURL = target.FormatURL()
		// 若填写了默认子目录却未找到，再回退检查桶根目录（或反之）
		if cleanSub != "" {
			rootTarget := target
			rootTarget.Key = "vault.json"
			fallbackURL = rootTarget.FormatURL()
		} else {
			servalTarget := target
			servalTarget.Key = "serval/vault.json"
			fallbackURL = servalTarget.FormatURL()
		}
		return fileURL, fallbackURL, kind, vendor, cleanSub, nil
	}

	if rawURL == "" {
		return "", "", "", "", "", errors.New("请填写同步地址")
	}
	fileURL, err = syncer.FileURLWithDir(rawURL, subDir)
	if err != nil {
		return "", "", "", "", "", err
	}
	// 原设备常按默认子目录 serval 上传，但恢复时用户常只贴根目录地址：找不到时自动尝试 serval/vault.json
	if subDir == "" && !strings.HasSuffix(strings.ToLower(rawURL), ".json") {
		if fb, fbErr := syncer.FileURLWithDir(rawURL, "serval"); fbErr == nil && fb != fileURL {
			fallbackURL = fb
		}
	}
	return fileURL, fallbackURL, "WebDAV", vendor, subDir, nil
}

// submitRestore 下载远端保险库。本地是空的才整份换上；已有数据时拒绝覆盖。
func (a *app) submitRestore() {
	p := &a.restore
	if p.busy {
		return
	}
	fileURL, fallbackURL, kind, vendor, subDir, err := p.buildRestoreTarget()
	if err != nil {
		p.err = err.Error()
		return
	}
	if a.vault == nil || !a.vault.Empty() {
		p.err = "这台设备已有数据，不能整份替换。请在设置的“同步”里开启同步，两边会按记录合并。"
		return
	}
	if a.post == nil {
		p.err = "窗口还没准备好，稍后再试。"
		return
	}
	user, password := strings.TrimSpace(p.user), p.password
	p.busy = true
	p.err = ""
	v := a.vault
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		usedURL := fileURL
		usedSubDir := subDir
		remote, err := newSyncRemote(fileURL, user, password)
		if err != nil {
			a.post(func() {
				if a.vault == v && p.open {
					p.busy = false
					p.err = err.Error()
				}
			})
			return
		}
		body, _, err := remote.Download(ctx)
		if errors.Is(err, syncer.ErrNotFound) && fallbackURL != "" {
			if fbRemote, fbErr := newSyncRemote(fallbackURL, user, password); fbErr == nil {
				if fbBody, _, dlErr := fbRemote.Download(ctx); dlErr == nil {
					body = fbBody
					err = nil
					usedURL = fallbackURL
					if syncer.IsS3URL(usedURL) {
						if target, parseErr := syncer.ParseS3URL(usedURL); parseErr == nil {
							if d := path.Dir(target.Key); d != "." && d != "/" {
								usedSubDir = d
							} else {
								usedSubDir = ""
							}
						}
					} else if usedSubDir == "" {
						usedSubDir = "serval"
					}
				}
			}
		}
		a.post(func() {
			p := &a.restore
			p.busy = false
			if a.vault != v || !p.open {
				return
			}
			if err != nil {
				if errors.Is(err, syncer.ErrNotFound) {
					p.err = "远端还没有保险库。如果这是第一台设备，请在设置里开启同步，把本机数据传上去。"
					return
				}
				p.err = syncUserMessage(err)
				return
			}
			if err := v.Adopt(body); err != nil {
				p.err = err.Error()
				return
			}
			if err := a.configureSyncWithMeta(usedURL, user, password, true, kind, vendor, usedSubDir); err != nil {
				p.err = err.Error()
				return
			}
			a.restore = restorePanel{}
			a.hosts, a.keys, a.proxies = nil, nil, nil
			a.settings = settings{}
			if a.vault.State() == vault.Locked {
				a.toast(ui.Toast{Title: "已下载保险库", Description: "输入原来的主密码即可解锁。"})
				return
			}
			a.reload()
			a.toast(ui.Toast{Title: "已从同步恢复"})
		})
	}()
}

func (a *app) syncPassword() string {
	if a.device == nil {
		return ""
	}
	if a.syncPassCached && a.syncPassDev == a.device {
		return a.syncPassValue
	}
	b, err := a.device.Get(syncSecretName)
	a.syncPassCached = true
	a.syncPassDev = a.device
	if err != nil {
		a.syncPassValue = ""
		return ""
	}
	a.syncPassValue = string(b)
	return a.syncPassValue
}

// onVaultReady 在保险库解锁并且本机设置已经加载后调用：开了同步就先对齐一次，之后每几分钟再对齐。
func (a *app) onVaultReady() {
	if !a.vaultOpen() || a.post == nil {
		return
	}
	if a.syncConfigured() {
		a.startSyncLoop()
		a.syncNow(false)
	}
	a.startSyncDir()
	if !a.local.DisableAutoCheckUpdate {
		a.checkForUpdates(false)
	}
}

// aiChatSyncURL 根据主 vault.json 的同步 URL 推导出同目录下独立 AI 会话分片（aichat.vault.json）的同步 URL。
func aiChatSyncURL(vaultFileURL string) (string, error) {
	if syncer.IsS3URL(vaultFileURL) {
		target, err := syncer.ParseS3URL(vaultFileURL)
		if err != nil {
			return "", err
		}
		dir := path.Dir(strings.TrimPrefix(target.Key, "/"))
		if dir == "." || dir == "/" || dir == "" {
			target.Key = "aichat.vault.json"
		} else {
			target.Key = dir + "/aichat.vault.json"
		}
		return target.FormatURL(), nil
	}
	u, err := url.Parse(vaultFileURL)
	if err != nil {
		return "", err
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "/" {
		u.Path = "/aichat.vault.json"
	} else {
		u.Path = dir + "/aichat.vault.json"
	}
	return u.String(), nil
}

// syncAIChatShard 在开启「同步 AI 会话」时同步独立的 aichat.vault.json 分片。
// 若本地分片尚无任何记录且远端也不存在，则不上传空分片。
func syncAIChatShard(ctx context.Context, aiV *vault.Vault, aiRemote syncer.Remote) error {
	if aiV == nil || aiV.State() != vault.Unlocked {
		return nil
	}
	if !aiV.HasRecords() {
		body, _, err := aiRemote.Download(ctx)
		if errors.Is(err, syncer.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil
		}
		_, err = aiV.Merge(body)
		return err
	}
	return syncer.Sync(ctx, aiV, aiRemote)
}

// noteVaultChanged 在本地写入成功后调用。短时间里的连续修改合成一次同步。
func (a *app) noteVaultChanged() {
	if !a.vaultOpen() {
		return
	}
	a.exportSyncDir()
	if !a.syncConfigured() {
		return
	}
	delay := a.syncDelay
	if delay <= 0 {
		delay = defaultSyncDelay
	}
	if a.syncTimer != nil {
		a.syncTimer.Stop()
	}
	a.syncTimer = time.AfterFunc(delay, func() {
		if a.post == nil {
			return
		}
		a.post(func() { a.syncNow(false) })
	})
}

// noteAIVaultChanged 在独立 AI 会话分片写入成功后调用；仅当开启「同步 AI 会话」时触发同步。
func (a *app) noteAIVaultChanged() {
	if !a.vaultOpen() || !a.local.SyncAIChats {
		return
	}
	a.noteVaultChanged()
}

func (a *app) startSyncLoop() {
	if a.syncLoopStop != nil {
		return
	}
	every := a.syncEvery
	if every <= 0 {
		every = defaultSyncEvery
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.syncLoopStop = cancel
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.post == nil {
					continue
				}
				a.post(func() { a.syncNow(false) })
			}
		}
	}()
}

func (a *app) stopSyncLoop() {
	if a.syncTimer != nil {
		a.syncTimer.Stop()
		a.syncTimer = nil
	}
	if a.syncLoopStop != nil {
		a.syncLoopStop()
		a.syncLoopStop = nil
	}
	a.syncAgain = false
	a.syncManual = false
}

// syncNow 立刻同步一次。manual 为真时把结果告诉用户；自动同步失败只记下错误，避免反复弹窗。
func (a *app) syncNow(manual bool) {
	if !a.vaultOpen() {
		if manual {
			a.toast(ui.Toast{Title: "请先解锁保险库", Description: "锁定时不能同步。"})
		}
		return
	}
	if !a.syncConfigured() {
		if manual {
			a.toast(ui.Toast{Title: "还没有开启同步", Description: "同步地址和账号记在这台设备上，密码只放在系统安全存储里。"})
		}
		return
	}
	if a.post == nil {
		return
	}
	if a.syncing {
		a.syncAgain = true
		if manual {
			a.syncManual = true
		}
		return
	}
	a.syncing = true
	fileURL, user, pass := a.local.SyncURL, a.local.SyncUser, a.syncPassword()
	syncAI := a.local.SyncAIChats
	v := a.vault
	aiV := a.aiVault
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		remote, err := newSyncRemote(fileURL, user, pass)
		mainSynced := false
		if err == nil {
			err = syncer.Sync(ctx, v, remote)
			if err == nil {
				mainSynced = true
			}
		}
		var aiErr error
		if mainSynced && syncAI && aiV != nil && aiV.State() == vault.Unlocked {
			if aiURL, urlErr := aiChatSyncURL(fileURL); urlErr != nil {
				aiErr = urlErr
			} else if aiRemote, rErr := newSyncRemote(aiURL, user, pass); rErr != nil {
				aiErr = rErr
			} else {
				aiErr = syncAIChatShard(ctx, aiV, aiRemote)
			}
		}
		a.post(func() {
			a.syncing = false
			if !a.vaultOpen() || a.vault != v {
				return
			}
			if err != nil {
				a.syncErr = syncUserMessage(err)
				a.syncDifferentVault = errors.Is(err, vault.ErrDifferentVault)
				if a.syncDifferentVault && a.vault.Empty() {
					a.syncErr = "远端是另一个保险库。这台设备还是空的，请用侧栏的“从同步恢复”。"
				}
				if manual {
					a.toast(ui.Toast{Title: "没能同步", Description: a.syncErr, Type: "error"})
				}
			} else {
				a.syncDifferentVault = false
				a.lastSync = time.Now()
				a.reload()
				if aiErr != nil {
					a.syncErr = "主保险库已同步，AI 会话分片同步失败：" + syncUserMessage(aiErr)
					if manual {
						a.toast(ui.Toast{Title: "AI 会话同步失败", Description: a.syncErr, Type: "error"})
					}
				} else {
					a.syncErr = ""
					if manual {
						a.toast(ui.Toast{Title: "已同步"})
					}
				}
			}
			if a.syncAgain {
				againManual := a.syncManual
				a.syncAgain, a.syncManual = false, false
				a.syncNow(againManual)
			}
		})
	}()
}

func syncUserMessage(err error) string {
	switch {
	case errors.Is(err, syncer.ErrAuth):
		detail := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(err.Error(), syncer.ErrAuth.Error()), ":"))
		if detail != "" {
			return detail
		}
		return "同步账号或密码不正确。"
	case errors.Is(err, syncer.ErrUnavailable):
		s := err.Error()
		if !strings.HasPrefix(s, "连不上同步服务") {
			s = "连不上同步服务: " + s
		}
		return s
	case errors.Is(err, syncer.ErrConflict):
		return "同步时和其他设备改到了一起，请稍后再试。"
	case errors.Is(err, syncer.ErrNoFolder):
		detail := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(err.Error(), syncer.ErrNoFolder.Error()), ":"))
		if detail != "" {
			return detail
		}
		return "同步地址下还没有这个文件夹，请先在服务器上建好。"
	case errors.Is(err, syncer.ErrNoETag):
		return "同步服务没有返回文件版本，为避免覆盖已取消上传。"
	case errors.Is(err, vault.ErrDifferentVault):
		return "远端是另一个保险库，不能合并到这台设备上。"
	case errors.Is(err, vault.ErrLocked):
		return "保险库已锁定，解锁后再同步。"
	default:
		msg := strings.TrimSpace(err.Error())
		if msg != "" {
			return msg
		}
		return "没能完成同步，本机的修改还在。"
	}
}
