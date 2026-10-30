import fs from 'node:fs';

function extract(readme, startRegex, endRegex) {
  const lines = readme.split('\n');
  const start = lines.findIndex((l) => startRegex.test(l));
  const end = lines.findIndex((l, i) => i > start && endRegex.test(l));
  return lines
    .slice(start + 1, end === -1 ? lines.length : end)
    .filter((l) => !l.trimStart().startsWith('<'))
    .join('\n')
    .trim();
}

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function renderInline(s) {
  let out = escapeHtml(s);
  out = out.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2">$1</a>');
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>');
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  return out;
}

function renderMarkdown(md) {
  const out = [];
  const list = [];
  let inCode = false;
  const code = [];

  const flushList = () => {
    if (list.length > 0) {
      out.push('<ul>' + list.map((i) => `<li>${renderInline(i)}</li>`).join('') + '</ul>');
      list.length = 0;
    }
  };

  for (const line of md.split('\n')) {
    if (line.startsWith('```')) {
      if (inCode) {
        out.push('<pre><code>' + escapeHtml(code.join('\n')) + '</code></pre>');
        code.length = 0;
        inCode = false;
      } else {
        flushList();
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      code.push(line);
      continue;
    }
    if (line.startsWith('- ')) {
      list.push(line.slice(2));
      continue;
    }
    flushList();
    if (line.trim() === '') continue;
    out.push('<p>' + renderInline(line) + '</p>');
  }
  flushList();
  return out.join('\n');
}

export function readmeSections() {
  const readme = fs.readFileSync(new URL('../../../README.md', import.meta.url), 'utf-8');
  return {
    explainer: renderMarkdown(extract(readme, /^<!--- website explainer START -->/, /^<!--- website explainer END -->/)),
    overview: renderMarkdown(extract(readme, /^<!--- website overview START -->/, /^<!--- website overview END -->/)),
    install: renderMarkdown(extract(readme, /^<!--- website install START -->/, /^<!--- website install END -->/))
  };
}
