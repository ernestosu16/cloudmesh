// Tema antes de pintar, sin parpadeo; lib/theme.ts usa la misma clave. Va en
// un archivo y no en línea para que la CSP de la app no admita scripts en línea.
try {
  var th = localStorage.getItem('cloudmesh.theme')
  if (th === 'dark' || (th !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches))
    document.documentElement.classList.add('dark')
} catch (e) {}
