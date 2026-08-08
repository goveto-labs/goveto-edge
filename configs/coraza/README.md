# Coraza / OWASP CRS

The edge WAF embeds `github.com/corazawaf/coraza-coreruleset/v4`; production
nodes do not need a separately mounted rules directory. Select the
`CORAZA_CRS` engine in a site's WAF policy to use the embedded rule set.