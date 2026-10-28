package homebrew

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

func (s *Store) Export(out io.Writer, repository string) error {
	if repository != "" {
		u, e := url.Parse(repository)
		if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 2 {
			return errors.New("repository must be https://github.com/OWNER/REPOSITORY")
		}
		repository = strings.TrimSuffix(repository, "/")
	}
	env, e := s.Catalog()
	if e != nil {
		return e
	}
	raw, _ := json.MarshalIndent(env, "", "  ")
	catalog, e := VerifyCatalog(raw, s.PublicKey(), 0)
	if e != nil {
		return e
	}
	z := zip.NewWriter(out)
	add := func(name string, b []byte) error {
		w, e := z.Create(name)
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	}
	for _, item := range []struct{ Name, Source string }{{"docs/index.html", "web/pages.html"}, {"docs/store.js", "web/pages.js"}, {"docs/style.css", "web/pages.css"}} {
		b, e := web.ReadFile(item.Source)
		if e != nil {
			return e
		}
		if e = add(item.Name, b); e != nil {
			return e
		}
	}
	legacy, e := s.CatalogFor(1)
	if e != nil {
		return e
	}
	legacyRaw, _ := json.MarshalIndent(legacy, "", "  ")
	if e = add("docs/catalog/v1.json", legacyRaw); e != nil {
		return e
	}
	if e = add("docs/catalog/v2.json", raw); e != nil {
		return e
	}
	if e = add("docs/public-key.txt", []byte(s.PublicKey()+"\n")); e != nil {
		return e
	}
	if e = add("docs/.nojekyll", nil); e != nil {
		return e
	}
	site, _ := json.Marshal(map[string]string{"repository": repository, "catalog": "catalog/v2.json"})
	if e = add("docs/site.json", site); e != nil {
		return e
	}
	for _, entry := range catalog.Entries {
		b, e := s.Download(entry.SHA256, false)
		if e != nil {
			return e
		}
		if e = add("docs/packages/"+entry.SHA256+".aliceplugin", b); e != nil {
			return e
		}
	}
	template := `name: Plugin submission
description: Submit an .aliceplugin package for manual review
title: "[Plugin] "
labels: []
body:
  - type: input
    id: package
    attributes:
      label: Link to the .aliceplugin release asset
      description: Upload the package to a GitHub Release in your source repository.
    validations:
      required: true
  - type: input
    id: source
    attributes:
      label: Source repository and commit
    validations:
      required: true
  - type: input
    id: checksum
    attributes:
      label: SHA-256 of the exact package
    validations:
      required: true
  - type: textarea
    id: tests
    attributes:
      label: Requested permissions, tests and device compatibility
    validations:
      required: true
  - type: checkboxes
    id: review
    attributes:
      label: Review process
      options:
        - label: I understand that publication requires manual approval of this exact version.
          required: true
`
	if e = add(".github/ISSUE_TEMPLATE/plugin.yml", []byte(template)); e != nil {
		return e
	}
	readme := "# Alice Homebrew plugin catalog\n\nUpload this export to the repository. Enable Settings > Pages > Deploy from a branch > main > /docs. The source of the GitHub Issues template belongs in the repository root.\n\nThe catalog is signed. Never publish catalog-signing.key, admin-token.txt or your moderation data directory. Re-export and publish after each approval or revocation. Catalog exports expire after one year.\n\nStation catalog URL: https://OWNER.github.io/REPOSITORY/catalog/v1.json\n\nPinned public key: " + s.PublicKey() + "\n"
	if e = add("README.md", []byte(readme)); e != nil {
		return e
	}
	return z.Close()
}
