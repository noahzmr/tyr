#!/usr/bin/env python3
"""Export an archify diagram (HTML) as a dual-theme SVG for the README.

    scripts/export-diagram.py docs/diagrams/architecture.html docs/images/architecture.svg

Does what the diagram's own "Download SVG" menu does, without a browser: the
inline SVG plus its CSS classes, dark colours by default and the light ones
under prefers-color-scheme: light, so GitHub and GitLab show the diagram in the
reader's theme. No network access; the font falls back to the local monospace.
"""
import re
import sys

SVG_RULE = re.compile(r'(^|,)\s*(svg|:root|\[data-theme|\.c-|\.t-|\.a-|\.m-)')
FONT = ("svg { font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, "
        "'DejaVu Sans Mono', 'Liberation Mono', monospace; }")


def rules(css):
    """Top-level (selector, body) pairs; @-rules (media, keyframes) are skipped."""
    css = re.sub(r'/\*.*?\*/', '', css, flags=re.S)
    out, i, n = [], 0, len(css)
    while i < n:
        j = css.find('{', i)
        if j < 0:
            break
        sel = css[i:j].strip()
        depth, k = 1, j + 1
        while k < n and depth:
            depth += {'{': 1, '}': -1}.get(css[k], 0)
            k += 1
        if not sel.startswith('@'):
            out.append((sel, css[j + 1:k - 1].strip()))
        i = k
    return out


def variables(body):
    return ' '.join(m.group(0).strip() for m in re.finditer(r'--[\w-]+\s*:[^;]+;', body))


def export(html):
    css = '\n'.join(re.findall(r'<style[^>]*>(.*?)</style>', html, re.S))
    host, dark, light = [], '', ''
    for sel, body in rules(css):
        if not SVG_RULE.search(sel):
            continue
        host.append(f'{sel} {{ {body} }}')
        if '[data-theme="light"]' in sel and ':root' not in sel:
            light += variables(body)
        elif ':root' in sel or '[data-theme="dark"]' in sel:
            dark += variables(body)
    if not dark or not light:
        sys.exit('export-diagram: theme variables not found')

    start = html.index('<svg', html.index('class="diagram-container"'))
    end = html.index('</svg>', start) + len('</svg>')
    svg = html[start:end]
    head_end = svg.index('>') + 1
    head = svg[:head_end]
    vb = [float(x) for x in re.search(r'viewBox="([^"]+)"', head).group(1).split()]
    head = re.sub(r'\s(width|height|data-theme)="[^"]*"', '', head)
    head = head[:-1] + f' xmlns="http://www.w3.org/2000/svg" width="{vb[2]:g}" height="{vb[3]:g}">'
    style = '\n'.join([
        FONT, *host,
        f':root, svg {{ {dark} }}',
        f'@media (prefers-color-scheme: light) {{ :root, svg {{ {light} }} }}',
        f'svg[data-theme="light"] {{ {light} }}',
        f'svg[data-theme="dark"] {{ {dark} }}',
        'rect.c-bg-rect { fill: var(--bg); }',
    ])
    return (head + f'<style>\n{style}\n</style><rect class="c-bg-rect" width="100%" height="100%"/>'
            + svg[head_end:] + '\n')


if __name__ == '__main__':
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    with open(sys.argv[1], encoding='utf-8') as f:
        out = export(f.read())
    with open(sys.argv[2], 'w', encoding='utf-8') as f:
        f.write(out)
