export default {
  customSyntax: 'postcss-html',
  rules: {
    // Vue SFC 允许 :global/:deep；纯 CSS 文件中的无效写法会在构建前被拦截。
    'selector-pseudo-class-no-unknown': [true, { ignorePseudoClasses: ['global', 'deep'] }]
  }
}
