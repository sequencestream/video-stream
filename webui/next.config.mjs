/** @type {import('next').NextConfig} */
const nextConfig = {
  // standalone keeps the production image small: only the traced runtime files
  // are copied instead of the whole node_modules tree.
  output: 'standalone',
};

export default nextConfig;
