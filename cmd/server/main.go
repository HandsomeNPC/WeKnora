// Package main is the main package for the WeKnora server
// It contains the main function and the entry point for the server
package main

import (
	"context"   //上下文管理
	"fmt"       //格式化输出
	"log"       //日志记录
	"net/http"  //HTTP服务器
	"os"        //操作系统接口
	"os/signal" //信号处理
	"syscall"   //系统调用
	"time"      //时间处理

	"github.com/gin-gonic/gin" //WEB框架

	//内部包
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/container"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/tracing"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func main() {
	// Set log format with request ID
	//初始化日志
	//设置日志格式:时间戳+微秒+短文件名
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	//输出到标准输出
	log.SetOutput(os.Stdout)

	// Set Gin mode
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// Build dependency injection container
	//构建依赖注入容器
	c := container.BuildContainer(runtime.GetContainer())

	// Run application
	err := c.Invoke(func(
		cfg *config.Config, //配置对象
		router *gin.Engine, //Gin路由引擎
		tracer *tracing.Tracer, //链路追踪
		testDataService *service.TestDataService, //测试数据服务
		resourceCleaner interfaces.ResourceCleaner, //资源清理器
	) error {
		//这里实现所有的启动逻辑
		// Create context for resource cleanup
		// 优雅关闭超时设置
		shutdownTimeout := cfg.Server.ShutdownTimeout
		if shutdownTimeout == 0 {
			shutdownTimeout = 30 * time.Second //默认30秒超时
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cleanupCancel()

		// Register tracer cleanup function to resource cleaner
		// 将链路追踪器注册到资源清理器
		resourceCleaner.RegisterWithName("Tracer", func() error {
			return tracer.Cleanup(cleanupCtx)
		})

		// Initialize test data
		//测试数据初始化
		if testDataService != nil {
			if err := testDataService.InitializeTestData(context.Background()); err != nil {
				log.Printf("Failed to initialize test data: %v", err)
			}
		}

		// Create HTTP server
		//创建HTTP服务器
		server := &http.Server{
			Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
			Handler: router, //使用依赖注入的Gin路由
		}

		ctx, done := context.WithCancel(context.Background())
		//信号处理-优雅关闭
		//信号监听
		signals := make(chan os.Signal, 1)
		//syscall.SIGINT Ctrl+C中断信号,
		//syscall.SIGTERM 终止信号(Docker stop),
		//syscall.SIGHUP 挂起信号
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		//优雅关闭流程
		go func() {
			sig := <-signals
			log.Printf("Received signal: %v, starting server shutdown...", sig)

			// Create a context with timeout for server shutdown
			//关闭HTTP服务器(停止接受新请求，等待现有请求完成)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			if err := server.Shutdown(shutdownCtx); err != nil {
				log.Fatalf("Server forced to shutdown: %v", err)
			}

			// Clean up all registered resources
			//清理所有注册的资源
			log.Println("Cleaning up resources...")
			errs := resourceCleaner.Cleanup(cleanupCtx)
			if len(errs) > 0 {
				log.Printf("Errors occurred during resource cleanup: %v", errs)
			}

			log.Println("Server has exited")
			done() //通知主流程关闭完成
		}()

		// Start server
		//启动服务
		log.Printf("Server is running at %s:%d", cfg.Server.Host, cfg.Server.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("failed to start server: %v", err)
		}

		// Wait for shutdown signal
		//等待关闭信号
		<-ctx.Done()
		return nil
	})
	if err != nil {
		log.Fatalf("Failed to run application: %v", err)
	}
}
