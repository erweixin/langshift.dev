export async function onRequest(context: { request: Request; next: () => Promise<Response> }): Promise<Response> {
  const { request, next } = context
  
  // 添加 CORS 头部
  const response = await next()
  
  // 设置 CORS 头部
  response.headers.set('Access-Control-Allow-Origin', '*')
  response.headers.set('Access-Control-Allow-Methods', 'GET, POST, PUT, DELETE, OPTIONS')
  response.headers.set('Access-Control-Allow-Headers', 'Content-Type, Authorization')
  
  return response
}
