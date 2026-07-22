const path = require('path');
const MiniCssExtractPlugin = require('mini-css-extract-plugin');
const HtmlWebpackPlugin = require('html-webpack-plugin');

module.exports = {
  entry: {
    buildoor: './src/index.tsx',
    overview: './src/overview.tsx',
  },
  output: {
    filename: 'bundle/[name].[contenthash].js',
    chunkFilename: 'bundle/[name].[contenthash].js',
    path: path.resolve(__dirname, 'static'),
    publicPath: '/',
    clean: true,
  },
  resolve: {
    extensions: ['.tsx', '.ts', '.js'],
  },
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        loader: 'esbuild-loader',
        options: {
          target: 'es2020',
        },
        exclude: /node_modules/,
      },
      {
        test: /\.(png|jpe?g|gif|svg|ico)$/i,
        type: 'asset/resource',
      },
      {
        test: /\.(woff2?|eot|ttf|otf)$/i,
        type: 'asset/resource',
      },
      {
        test: /\.css$/,
        use: [MiniCssExtractPlugin.loader, 'css-loader'],
      },
    ],
  },
  plugins: [
    new MiniCssExtractPlugin({
      filename: 'bundle/[name].[contenthash].css',
      chunkFilename: 'bundle/[name].[contenthash].css',
    }),
    new HtmlWebpackPlugin({
      template: './src/index.html',
      filename: 'index.html',
      favicon: './src/assets/buildoor.ico',
      inject: 'body',
      scriptLoading: 'defer',
      chunks: ['buildoor'],
    }),
    new HtmlWebpackPlugin({
      template: './src/overview.html',
      filename: 'overview.html',
      favicon: './src/assets/buildoor.ico',
      inject: 'body',
      scriptLoading: 'defer',
      chunks: ['overview'],
    }),
  ],
  performance: {
    hints: false,
  },
};
