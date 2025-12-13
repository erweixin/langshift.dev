import { NextRequest, NextResponse } from 'next/server'

export async function POST(request: NextRequest) {
  try {
    const { code } = await request.json()

    if (!code || typeof code !== 'string') {
      return NextResponse.json(
        { error: '代码内容不能为空' },
        { status: 400 }
      )
    }

    // Paiza.io API 配置
    const apiUrl = 'https://api.paiza.io/runners/create.json'
    const getUrl = 'https://api.paiza.io/runners/get_details.json'
    
    // 创建运行任务（添加超时和重试机制）
    let createResponse
    try {
      createResponse = await fetch(apiUrl, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
          'User-Agent': 'LangShift-Java-Runner/1.0',
        },
        body: new URLSearchParams({
          source_code: code,
          language: 'java',
          input: '',
          longpoll: 'false', // 改为 false，避免长连接问题
          api_key: 'guest'
        }),
        // 添加超时配置
        signal: AbortSignal.timeout(15000) // 15秒超时
      })
    } catch (fetchError: any) {
      console.error('网络请求失败:', fetchError)
      if (fetchError.name === 'TimeoutError') {
        return NextResponse.json({
          output: '',
          error: '请求超时，请检查网络连接后重试'
        }, { status: 408 })
      }
      return NextResponse.json({
        output: '',
        error: '网络连接失败，请稍后重试'
      }, { status: 503 })
    }
    
    if (!createResponse.ok) {
      console.error(`Paiza.io API 请求失败: ${createResponse.status}`)
      return NextResponse.json({
        output: '',
        error: `代码执行服务暂时不可用，请稍后重试`
      }, { status: 503 })
    }
    
    const createResult = await createResponse.json()
    
    if (createResult.error) {
      return NextResponse.json({
        output: '',
        error: `编译错误: ${createResult.error}`
      })
    }
    
    const id = createResult.id
    
    // 轮询获取结果
    let attempts = 0
    const maxAttempts = 30 // 最多等待30秒
    
    while (attempts < maxAttempts) {
      await new Promise(resolve => setTimeout(resolve, 1000)) // 等待1秒
      
      let getResponse
      try {
        getResponse = await fetch(`${getUrl}?id=${id}&api_key=guest`, {
          headers: {
            'User-Agent': 'LangShift-Java-Runner/1.0',
          },
          signal: AbortSignal.timeout(10000) // 10秒超时
        })
      } catch (fetchError: any) {
        console.error(`获取结果网络请求失败 (尝试 ${attempts + 1}/${maxAttempts}):`, fetchError)
        attempts++
        if (attempts >= maxAttempts) {
          return NextResponse.json({
            output: '',
            error: '网络连接不稳定，请稍后重试'
          }, { status: 503 })
        }
        continue // 继续重试
      }
      
      if (!getResponse.ok) {
        console.error(`获取结果失败: ${getResponse.status}`)
        attempts++
        if (attempts >= maxAttempts) {
          return NextResponse.json({
            output: '',
            error: `代码执行服务暂时不可用，请稍后重试`
          })
        }
        continue // 继续重试
      }
      
      // 正确处理 ReadableStream 响应
      let getResult
      try {
        // 优先使用 text() 方法处理 ReadableStream，然后解析 JSON
        const responseText = await getResponse.text()
        getResult = JSON.parse(responseText)
      } catch (parseError: any) {
        console.error('JSON 解析失败:', parseError)
        console.error('响应状态:', getResponse.status, getResponse.statusText)
        return NextResponse.json({
          output: '',
          error: `API 响应解析失败，请稍后重试`
        })
      }
      
      if (getResult.status === 'completed') {
        // 检查是否有错误
        if (getResult.stderr) {
          return NextResponse.json({
            output: getResult.stdout || '',
            error: getResult.stderr
          })
        }
        
        return NextResponse.json({
          output: getResult.stdout || '',
          error: null
        })
      } else if (getResult.status === 'error') {
        return NextResponse.json({
          output: '',
          error: `执行错误: ${getResult.stderr || '未知错误'}`
        })
      }
      
      attempts++
    }
    
    return NextResponse.json({
      output: '',
      error: '执行超时，请稍后重试'
    })
    
  } catch (error: any) {
    console.error('Java API 错误:', error)
    return NextResponse.json(
      { 
        output: '', 
        error: `Java 执行失败: ${error.message}` 
      },
      { status: 500 }
    )
  }
}

 