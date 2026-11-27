package deploy

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ngocp/goterm-control/internal/connectors"
)

// A static site on AWS: a private S3 bucket behind a CloudFront distribution.

// cachingOptimized is CloudFront's managed cache policy of that name.
const cachingOptimized = "658327ea-f89d-4fab-a63d-7e88639e58f6"

func distComment(name string) string { return "bomclaw:" + name }

var (
	distMu    sync.Mutex
	distAt    time.Time
	distCache []cftypes.DistributionSummary
)

// distributions lists the account's CloudFront distributions, briefly cached:
// every room's status asks, and the list changes only on a deploy.
func (a *AWS) distributions(ctx context.Context, fresh bool) ([]cftypes.DistributionSummary, error) {
	distMu.Lock()
	if !fresh && distCache != nil && time.Since(distAt) < 30*time.Second {
		out := distCache
		distMu.Unlock()
		return out, nil
	}
	distMu.Unlock()
	cfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		return nil, err
	}
	cf := cloudfront.NewFromConfig(cfg)
	var out []cftypes.DistributionSummary
	var marker *string
	for {
		page, err := cf.ListDistributions(ctx, &cloudfront.ListDistributionsInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("cloudfront: %w", err)
		}
		if page.DistributionList == nil {
			break
		}
		out = append(out, page.DistributionList.Items...)
		if !aws.ToBool(page.DistributionList.IsTruncated) {
			break
		}
		marker = page.DistributionList.NextMarker
	}
	distMu.Lock()
	distCache, distAt = out, time.Now()
	distMu.Unlock()
	return out, nil
}

func (a *AWS) findDistribution(ctx context.Context, name string, fresh bool) (*cftypes.DistributionSummary, error) {
	all, err := a.distributions(ctx, fresh)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if aws.ToString(all[i].Comment) == distComment(name) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// staticStatus fills st from the project's distribution and bucket.
func (a *AWS) staticStatus(ctx context.Context, name string, st *ProdStatus) {
	d, err := a.findDistribution(ctx, name, false)
	if err != nil {
		st.Error = err.Error()
		return
	}
	if d == nil {
		return // never deployed
	}
	st.Deployed = true
	st.WorkersDev = "https://" + aws.ToString(d.DomainName)
	st.URL = st.WorkersDev
	if d.Aliases != nil {
		st.Domains = d.Aliases.Items
	}
	if d.LastModifiedTime != nil {
		st.DeployedAt = d.LastModifiedTime.UTC().Format(time.RFC3339)
	}
	if s := aws.ToString(d.Status); s != "" && s != "Deployed" {
		st.Services = append(st.Services, "CloudFront: "+s)
	}
	acct, err := a.accountID(ctx)
	if err != nil {
		return
	}
	cfg, err := a.config(ctx)
	if err != nil {
		return
	}
	tags, err := s3.NewFromConfig(cfg).GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucketName(name, acct))})
	if err != nil {
		return
	}
	v := &ProdVersion{By: "bomclaw"}
	for _, t := range tags.TagSet {
		switch aws.ToString(t.Key) {
		case "bomclaw:commit":
			v.Commit = aws.ToString(t.Value)
		case "bomclaw:message":
			v.Message = aws.ToString(t.Value)
		case "bomclaw:deployed_at":
			st.DeployedAt = aws.ToString(t.Value)
		case "bomclaw:version":
			v.ID = aws.ToString(t.Value)
		}
	}
	st.Version = v
}

// contentType names a file's type for S3, which serves it as stored.
func contentType(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// cacheControl: HTML is checked every time, so a publish is seen at once;
// everything else is cached for an hour at the edge and in the browser.
func cacheControl(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".html") {
		return "no-cache"
	}
	return "public, max-age=3600"
}

func (a *AWS) deployStatic(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	step := func(format string, x ...any) { fmt.Fprintf(log, "▸ "+format+"\n", x...) }
	res := &Result{DryRun: opt.DryRun}
	vars := projectConnectors(r, step)
	if r.Build != "" {
		step("build: %s", r.Build)
		if err := ExecRunner(ctx, r.WorkDir, connectors.BuildEnv(vars), log, "sh", "-c", r.Build); err != nil {
			return nil, fmt.Errorf("build failed: %w", err)
		}
	}
	files, err := collectFiles(r.StaticDir, false)
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("%s is empty or missing — nothing to publish (run the build, or fix deploy.static)", r.StaticDir)
	}
	if hasSecret(vars) {
		step("connectors: this is a static site, so only public values were built in; secret keys are not sent")
	}
	step("static: %d files from %s", len(files), r.StaticDir)
	if opt.DryRun {
		step("dry run: would sync to S3 bucket bomclaw-%s-<account> behind CloudFront", r.Name)
		return res, nil
	}

	acct, err := a.accountID(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := a.config(ctx)
	if err != nil {
		return nil, err
	}
	region := cfg.Region
	bucket := bucketName(r.Name, acct)
	s3c := s3.NewFromConfig(cfg)

	// The bucket: private, in the project's region.
	if _, err := s3c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket}); err != nil {
		in := &s3.CreateBucketInput{Bucket: &bucket}
		if region != globalRegion {
			in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(region)}
		}
		if _, err := s3c.CreateBucket(ctx, in); err != nil {
			return nil, fmt.Errorf("create bucket %s: %w", bucket, err)
		}
		step("s3: created bucket %s (%s, private)", bucket, region)
	}

	// Sync: upload what changed, delete what is gone.
	have := map[string]string{}
	pager := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{Bucket: &bucket})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", bucket, err)
		}
		for _, o := range page.Contents {
			have[aws.ToString(o.Key)] = strings.Trim(aws.ToString(o.ETag), `"`)
		}
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		upErr    error
		uploaded int
		sem      = make(chan struct{}, 8)
	)
	want := map[string]bool{}
	for _, f := range files {
		want[f.File] = true
		b, err := os.ReadFile(f.path)
		if err != nil {
			return nil, err
		}
		sum := md5.Sum(b)
		if have[f.File] == hex.EncodeToString(sum[:]) {
			continue
		}
		wg.Add(1)
		go func(key string, b []byte) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, err := s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: aws.String(key), Body: bytes.NewReader(b),
				ContentType: aws.String(contentType(key)), CacheControl: aws.String(cacheControl(key))})
			mu.Lock()
			defer mu.Unlock()
			if err != nil && upErr == nil {
				upErr = fmt.Errorf("upload %s: %w", key, err)
			}
			uploaded++
		}(f.File, b)
	}
	wg.Wait()
	if upErr != nil {
		return nil, upErr
	}
	var stale []s3types.ObjectIdentifier
	for k := range have {
		if !want[k] {
			stale = append(stale, s3types.ObjectIdentifier{Key: aws.String(k)})
		}
	}
	removed := len(stale)
	for len(stale) > 0 {
		n := min(len(stale), 1000)
		if _, err := s3c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &bucket, Delete: &s3types.Delete{Objects: stale[:n], Quiet: aws.Bool(true)}}); err != nil {
			return nil, fmt.Errorf("delete stale files: %w", err)
		}
		stale = stale[n:]
	}
	step("s3: %d uploaded, %d unchanged, %d removed", uploaded, len(files)-uploaded, removed)

	// The distribution in front of it.
	gcfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		return nil, err
	}
	cf := cloudfront.NewFromConfig(gcfg)
	d, err := a.findDistribution(ctx, r.Name, true)
	if err != nil {
		return nil, err
	}
	var distID, distARN, distDomain string
	if d == nil {
		oac, err := ensureOAC(ctx, cf, "bomclaw-"+r.Name)
		if err != nil {
			return nil, err
		}
		out, err := cf.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{DistributionConfig: newDistConfig(r, bucket, region, oac)})
		if err != nil {
			return nil, fmt.Errorf("create CloudFront distribution: %w", err)
		}
		distID, distARN, distDomain = aws.ToString(out.Distribution.Id), aws.ToString(out.Distribution.ARN), aws.ToString(out.Distribution.DomainName)
		step("cloudfront: created %s (%s) — the first rollout takes a few minutes", distID, distDomain)
	} else {
		distID, distARN, distDomain = aws.ToString(d.Id), aws.ToString(d.ARN), aws.ToString(d.DomainName)
		if _, err := cf.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{DistributionId: &distID,
			InvalidationBatch: &cftypes.InvalidationBatch{CallerReference: aws.String(fmt.Sprint(time.Now().UnixNano())),
				Paths: &cftypes.Paths{Quantity: aws.Int32(1), Items: []string{"/*"}}}}); err != nil {
			return nil, fmt.Errorf("cloudfront invalidation: %w", err)
		}
		step("cloudfront: %s, cache invalidated", distID)
	}

	// Only this distribution may read the bucket.
	policy, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid": "bomclawCloudFront", "Effect": "Allow",
			"Principal": map[string]string{"Service": "cloudfront.amazonaws.com"},
			"Action":    "s3:GetObject", "Resource": "arn:aws:s3:::" + bucket + "/*",
			"Condition": map[string]any{"StringEquals": map[string]string{"AWS:SourceArn": distARN}},
		}},
	})
	if _, err := s3c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: &bucket, Policy: aws.String(string(policy))}); err != nil {
		return nil, fmt.Errorf("bucket policy: %w", err)
	}

	// The version, on the bucket.
	g := Git(ctx, r.ProjectDir)
	tags := []s3types.Tag{
		{Key: aws.String("bomclaw:project"), Value: aws.String(r.Name)},
		{Key: aws.String("bomclaw:deployed_at"), Value: aws.String(time.Now().UTC().Format(time.RFC3339))},
		{Key: aws.String("bomclaw:version"), Value: aws.String(fmt.Sprint(time.Now().Unix()))},
	}
	if g != nil {
		tags = append(tags, s3types.Tag{Key: aws.String("bomclaw:commit"), Value: aws.String(g.Short)},
			s3types.Tag{Key: aws.String("bomclaw:message"), Value: aws.String(tagValue(deployMessage(g)))})
	}
	if _, err := s3c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: &bucket, Tagging: &s3types.Tagging{TagSet: tags}}); err != nil {
		step("tags: %v (the version will show as unknown)", err)
	}

	res.URL = "https://" + distDomain
	if r.Domain != "" {
		if a.attachCloudFrontDomain(ctx, cf, distID, distDomain, r.Domain, step) {
			res.URL = "https://" + r.Domain
		}
	}
	distMu.Lock()
	distCache = nil
	distMu.Unlock()
	step("live: %s", res.URL)
	return res, nil
}

func ensureOAC(ctx context.Context, cf *cloudfront.Client, name string) (string, error) {
	list, err := cf.ListOriginAccessControls(ctx, &cloudfront.ListOriginAccessControlsInput{})
	if err == nil && list.OriginAccessControlList != nil {
		for _, o := range list.OriginAccessControlList.Items {
			if aws.ToString(o.Name) == name {
				return aws.ToString(o.Id), nil
			}
		}
	}
	out, err := cf.CreateOriginAccessControl(ctx, &cloudfront.CreateOriginAccessControlInput{
		OriginAccessControlConfig: &cftypes.OriginAccessControlConfig{
			Name:                          aws.String(name),
			OriginAccessControlOriginType: cftypes.OriginAccessControlOriginTypesS3,
			SigningBehavior:               cftypes.OriginAccessControlSigningBehaviorsAlways,
			SigningProtocol:               cftypes.OriginAccessControlSigningProtocolsSigv4,
		},
	})
	if err != nil {
		return "", fmt.Errorf("origin access control: %w", err)
	}
	return aws.ToString(out.OriginAccessControl.Id), nil
}

func newDistConfig(r Resolved, bucket, region, oac string) *cftypes.DistributionConfig {
	origin := "s3-" + bucket
	methods := []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead}
	c := &cftypes.DistributionConfig{
		CallerReference:   aws.String(fmt.Sprintf("bomclaw-%s-%d", r.Name, time.Now().UnixNano())),
		Comment:           aws.String(distComment(r.Name)),
		Enabled:           aws.Bool(true),
		DefaultRootObject: aws.String("index.html"),
		HttpVersion:       cftypes.HttpVersionHttp2and3,
		PriceClass:        cftypes.PriceClassPriceClass200,
		Origins: &cftypes.Origins{Quantity: aws.Int32(1), Items: []cftypes.Origin{{
			Id:                    aws.String(origin),
			DomainName:            aws.String(fmt.Sprintf("%s.s3.%s.amazonaws.com", bucket, region)),
			OriginAccessControlId: aws.String(oac),
			S3OriginConfig:        &cftypes.S3OriginConfig{OriginAccessIdentity: aws.String("")},
		}}},
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:       aws.String(origin),
			ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyRedirectToHttps,
			CachePolicyId:        aws.String(cachingOptimized),
			Compress:             aws.Bool(true),
			AllowedMethods: &cftypes.AllowedMethods{Quantity: aws.Int32(2), Items: methods,
				CachedMethods: &cftypes.CachedMethods{Quantity: aws.Int32(2), Items: methods}},
		},
		ViewerCertificate: &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: aws.Bool(true)},
	}
	if r.SPA {
		// A private bucket answers 403 for a missing key; both mean "a
		// route the app knows", served as index.html.
		var items []cftypes.CustomErrorResponse
		for _, code := range []int32{403, 404} {
			items = append(items, cftypes.CustomErrorResponse{ErrorCode: aws.Int32(code), ResponseCode: aws.String("200"),
				ResponsePagePath: aws.String("/index.html"), ErrorCachingMinTTL: aws.Int64(10)})
		}
		c.CustomErrorResponses = &cftypes.CustomErrorResponses{Quantity: aws.Int32(int32(len(items))), Items: items}
	}
	return c
}

// attachCloudFrontDomain gets the domain a certificate (ACM, us-east-1,
// validated through DNS), adds it to the distribution and points it there.
// It reports whether the domain is attached.
func (a *AWS) attachCloudFrontDomain(ctx context.Context, cf *cloudfront.Client, distID, distDomain, domain string, step func(string, ...any)) bool {
	gcfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		step("domain: %v", err)
		return false
	}
	arn, err := ensureCert(ctx, acm.NewFromConfig(gcfg), domain, step)
	if err != nil {
		step("domain %s: %v", domain, err)
		return false
	}
	if arn == "" {
		step("domain %s: certificate pending validation — the next publish attaches it", domain)
		return false
	}
	got, err := cf.GetDistributionConfig(ctx, &cloudfront.GetDistributionConfigInput{Id: &distID})
	if err != nil {
		step("domain: %v", err)
		return false
	}
	dc := got.DistributionConfig
	already := dc.Aliases != nil && len(dc.Aliases.Items) == 1 && dc.Aliases.Items[0] == domain &&
		dc.ViewerCertificate != nil && aws.ToString(dc.ViewerCertificate.ACMCertificateArn) == arn
	if !already {
		dc.Aliases = &cftypes.Aliases{Quantity: aws.Int32(1), Items: []string{domain}}
		dc.ViewerCertificate = &cftypes.ViewerCertificate{ACMCertificateArn: aws.String(arn),
			SSLSupportMethod: cftypes.SSLSupportMethodSniOnly, MinimumProtocolVersion: cftypes.MinimumProtocolVersionTLSv122021}
		if _, err := cf.UpdateDistribution(ctx, &cloudfront.UpdateDistributionInput{Id: &distID, IfMatch: got.ETag, DistributionConfig: dc}); err != nil {
			step("domain %s: %v", domain, err)
			return false
		}
		step("cloudfront: %s now answers for %s", distID, domain)
	}
	pointDNS(ctx, []DNSRecord{{Type: "CNAME", Name: domain, Content: distDomain}}, step)
	return true
}

// ensureCert finds or requests the domain's certificate, writes its
// validation record, and waits a few minutes for it. "" means still pending.
func ensureCert(ctx context.Context, c *acm.Client, domain string, step func(string, ...any)) (string, error) {
	var arn string
	list, err := c.ListCertificates(ctx, &acm.ListCertificatesInput{CertificateStatuses: []acmtypes.CertificateStatus{
		acmtypes.CertificateStatusIssued, acmtypes.CertificateStatusPendingValidation}})
	if err != nil {
		return "", fmt.Errorf("acm: %w", err)
	}
	for _, s := range list.CertificateSummaryList {
		if aws.ToString(s.DomainName) == domain {
			arn = aws.ToString(s.CertificateArn)
			if s.Status == acmtypes.CertificateStatusIssued {
				return arn, nil
			}
		}
	}
	if arn == "" {
		out, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{DomainName: aws.String(domain), ValidationMethod: acmtypes.ValidationMethodDns})
		if err != nil {
			return "", fmt.Errorf("request certificate: %w", err)
		}
		arn = aws.ToString(out.CertificateArn)
		step("acm: requested a certificate for %s", domain)
	}
	// The validation record appears a few seconds after the request.
	var rec *acmtypes.ResourceRecord
	_ = waitFor(ctx, 3*time.Second, 45*time.Second, func() (bool, error) {
		d, err := c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: &arn})
		if err != nil {
			return false, err
		}
		for _, o := range d.Certificate.DomainValidationOptions {
			if o.ResourceRecord != nil {
				rec = o.ResourceRecord
				return true, nil
			}
		}
		return false, nil
	})
	if rec == nil {
		return "", fmt.Errorf("ACM has not said how to validate %s yet", domain)
	}
	pointDNS(ctx, []DNSRecord{{Type: string(rec.Type), Name: aws.ToString(rec.Name), Content: aws.ToString(rec.Value)}}, step)
	step("acm: waiting for %s to be validated (up to 5 minutes)", domain)
	err = waitFor(ctx, 10*time.Second, 5*time.Minute, func() (bool, error) {
		d, err := c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: &arn})
		if err != nil {
			return false, err
		}
		return d.Certificate.Status == acmtypes.CertificateStatusIssued, nil
	})
	if errors.Is(err, errTimeout) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return arn, nil
}
