/** @type {import('next').NextConfig} */
const nextConfig = {
  // A static export rather than a Node server. The pages are pre-rendered at
  // build time and embedded into the vsd binary, so the product ships as one
  // executable and the UI cannot drift out of sync with the API it was built
  // against. Nothing here may use server-side rendering or route handlers.
  output: 'export',
  // Emit <route>/index.html for every route, which the Go file server resolves
  // without any URL rewriting of its own.
  trailingSlash: true,
  // There is no image optimiser in the binary.
  images: { unoptimized: true },
};

export default nextConfig;
