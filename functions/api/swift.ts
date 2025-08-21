interface Env {
  // 如果有环境变量的话在这里定义
}

export async function onRequestPost(context: { request: Request; env: Env }): Promise<Response> {
  try {
    const { request } = context
    
    // 解析请求体
    const { code } = await request.json()

    if (!code || typeof code !== 'string') {
      return new Response(
        JSON.stringify({ error: '代码内容不能为空' }),
        { 
          status: 400,
          headers: {
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*',
            'Access-Control-Allow-Methods': 'POST, OPTIONS',
            'Access-Control-Allow-Headers': 'Content-Type',
          }
        }
      )
    }

    // Paiza.io API 配置
    const apiUrl = 'https://api.paiza.io:443/runners/create.json'
    const getUrl = 'https://api.paiza.io:443/runners/get_details.json'
    
    // 创建运行任务
    const createResponse = await fetch(apiUrl, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
      },
      body: new URLSearchParams({
        source_code: code,
        language: 'swift',
        input: '',
        longpoll: 'true',
        longpoll_timeout: '10',
        api_key: 'guest'
      })
    })
    
    if (!createResponse.ok) {
      throw new Error(`Paiza.io API 请求失败: ${createResponse.status}`)
    }
    
    const createResult = await createResponse.json()
    
    if (createResult.error) {
      return new Response(
        JSON.stringify({
          output: '',
          error: `编译错误: ${createResult.error}`
        }),
        { 
          headers: {
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*',
            'Access-Control-Allow-Methods': 'POST, OPTIONS',
            'Access-Control-Allow-Headers': 'Content-Type',
          }
        }
      )
    }
    
    const id = createResult.id
    
    // 轮询获取结果
    let attempts = 0
    const maxAttempts = 30 // 最多等待30秒
    
    while (attempts < maxAttempts) {
      await new Promise(resolve => setTimeout(resolve, 1000)) // 等待1秒
      
      const getResponse = await fetch(`${getUrl}?id=${id}&api_key=guest`)
      
      if (!getResponse.ok) {
        throw new Error(`获取结果失败: ${getResponse.status}`)
      }
      
      const getResult = await getResponse.json()
      
      if (getResult.status === 'completed') {
        // 检查是否有错误
        if (getResult.stderr) {
          return new Response(
            JSON.stringify({
              output: getResult.stdout || '',
              error: getResult.stderr
            }),
            { 
              headers: {
                'Content-Type': 'application/json',
                'Access-Control-Allow-Origin': '*',
                'Access-Control-Allow-Methods': 'POST, OPTIONS',
                'Access-Control-Allow-Headers': 'Content-Type',
              }
            }
          )
        }
        
        return new Response(
          JSON.stringify({
            output: getResult.stdout || '',
            error: null
          }),
          { 
            headers: {
              'Content-Type': 'application/json',
              'Access-Control-Allow-Origin': '*',
              'Access-Control-Allow-Methods': 'POST, OPTIONS',
              'Access-Control-Allow-Headers': 'Content-Type',
            }
          }
        )
      } else if (getResult.status === 'error') {
        return new Response(
          JSON.stringify({
            output: '',
            error: `执行错误: ${getResult.stderr || '未知错误'}`
          }),
          { 
            headers: {
              'Content-Type': 'application/json',
              'Access-Control-Allow-Origin': '*',
              'Access-Control-Allow-Methods': 'POST, OPTIONS',
              'Access-Control-Allow-Headers': 'Content-Type',
            }
          }
        )
      }
      
      attempts++
    }
    
    return new Response(
      JSON.stringify({
        output: '',
        error: '执行超时，请稍后重试'
      }),
      { 
        headers: {
          'Content-Type': 'application/json',
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'POST, OPTIONS',
          'Access-Control-Allow-Headers': 'Content-Type',
        }
      }
    )
    
  } catch (error: any) {
    console.error('Swift API 错误:', error)
    return new Response(
      JSON.stringify({ 
        output: '', 
        error: `Swift 执行失败: ${error.message}` 
      }),
      { 
        status: 500,
        headers: {
          'Content-Type': 'application/json',
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'POST, OPTIONS',
          'Access-Control-Allow-Headers': 'Content-Type',
        }
      }
    )
  }
}

// 处理 CORS 预检请求
export async function onRequestOptions(): Promise<Response> {
  return new Response(null, {
    headers: {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'POST, OPTIONS',
      'Access-Control-Allow-Headers': 'Content-Type',
    }
  })
}
